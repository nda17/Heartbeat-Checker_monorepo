package smtp

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/netip"
	stdsmtp "net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

type DeliveryError struct {
	stage     string
	permanent bool
}

func (e *DeliveryError) Error() string   { return "SMTP " + e.stage + " failed" }
func (e *DeliveryError) Permanent() bool { return e.permanent }

func deliveryError(stage string, err error) error {
	var smtpErr *textproto.Error
	return &DeliveryError{stage: stage, permanent: errors.As(err, &smtpErr) && smtpErr.Code >= 500 && smtpErr.Code < 600}
}

type JobSender struct {
	addr, host, from, username, password, mode string
	tlsConfig                                  *tls.Config // package-local test seam; production always uses verified system roots
}

func NewProduction(addr, from, username, password, mode string) (*JobSender, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return nil, errors.New("SMTP_ADDR must be host:port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return nil, errors.New("SMTP_ADDR port is invalid")
	}
	if strings.ContainsAny(host, " \t\r\n%") {
		return nil, errors.New("SMTP_ADDR host is invalid")
	}
	if ip, err := netip.ParseAddr(host); err == nil && (ip.IsUnspecified() || ip.IsLoopback()) {
		return nil, errors.New("production SMTP must not use loopback")
	}
	if validAddress(from) != nil {
		return nil, errors.New("SMTP_FROM is invalid")
	}
	if username == "" || password == "" || strings.ContainsAny(username, "\r\n") {
		return nil, errors.New("SMTP credentials are required")
	}
	if mode != "implicit_tls" && mode != "starttls" {
		return nil, errors.New("SMTP_TLS_MODE must be implicit_tls or starttls")
	}
	return &JobSender{addr: addr, host: host, from: from, username: username, password: password, mode: mode}, nil
}

func (s *JobSender) SendWithID(ctx context.Context, to, subject, body, messageID string) error {
	if validAddress(to) != nil || strings.ContainsAny(subject, "\r\n") || !validMessageID(messageID) {
		return &DeliveryError{stage: "message", permanent: true}
	}
	message, err := buildMessageWithID(s.from, to, subject, body, messageID)
	if err != nil {
		return &DeliveryError{stage: "message", permanent: true}
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	conn, err := (&net.Dialer{Timeout: sendTimeout}).DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return deliveryError("connect", err)
	}
	defer conn.Close()
	if err = conn.SetDeadline(time.Now().Add(sendTimeout)); err != nil {
		return deliveryError("deadline", err)
	}
	rawConn := conn
	stop := context.AfterFunc(ctx, func() { _ = rawConn.Close() })
	defer stop()
	tlsConfig := &tls.Config{ServerName: s.host, MinVersion: tls.VersionTLS12}
	if s.tlsConfig != nil {
		tlsConfig = s.tlsConfig.Clone()
	}
	if s.mode == "implicit_tls" {
		secure := tls.Client(conn, tlsConfig)
		if err = secure.HandshakeContext(ctx); err != nil {
			return deliveryError("TLS handshake", err)
		}
		conn = secure
	}
	client, err := stdsmtp.NewClient(conn, s.host)
	if err != nil {
		return deliveryError("greeting", err)
	}
	defer client.Close()
	if err = client.Hello("localhost"); err != nil {
		return deliveryError("hello", err)
	}
	if s.mode == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return &DeliveryError{stage: "STARTTLS unavailable", permanent: true}
		}
		if err = client.StartTLS(tlsConfig); err != nil {
			return deliveryError("STARTTLS", err)
		}
	}
	if ok, _ := client.Extension("AUTH"); !ok {
		return &DeliveryError{stage: "AUTH unavailable", permanent: true}
	}
	if err = client.Auth(stdsmtp.PlainAuth("", s.username, s.password, s.host)); err != nil {
		return deliveryError("authentication", err)
	}
	if err = client.Mail(s.from); err != nil {
		return deliveryError("sender", err)
	}
	if err = client.Rcpt(to); err != nil {
		return deliveryError("recipient", err)
	}
	writer, err := client.Data()
	if err != nil {
		return deliveryError("data", err)
	}
	if _, err = writer.Write(message); err != nil {
		_ = writer.Close()
		return deliveryError("write", err)
	}
	if err = writer.Close(); err != nil {
		return deliveryError("commit", err)
	}
	_ = client.Quit()
	return nil
}

func validMessageID(id string) bool {
	if len(id) < 3 || len(id) > 255 || id[0] != '<' || id[len(id)-1] != '>' {
		return false
	}
	return !strings.ContainsAny(id, "\r\n \t") && strings.Count(id, "@") == 1
}

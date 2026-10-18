package smtp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/netip"
	stdsmtp "net/smtp"
	"strconv"
	"strings"
	"time"
)

const sendTimeout = 5 * time.Second

// Sender is limited to a loopback SMTP receiver for local development.
type Sender struct {
	addr string
	host string
	from string
}

func New(addr, from string) (*Sender, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return nil, errors.New("SMTP address must be a loopback host:port")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return nil, errors.New("SMTP address must use a loopback IP")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return nil, errors.New("SMTP port must be between 1 and 65535")
	}
	if err := validAddress(from); err != nil {
		return nil, fmt.Errorf("SMTP from address: %w", err)
	}
	return &Sender{addr: addr, host: host, from: from}, nil
}

func (s *Sender) Send(ctx context.Context, recipient, subject, body string) error {
	if err := validAddress(recipient); err != nil {
		return fmt.Errorf("SMTP recipient: %w", err)
	}
	if strings.ContainsAny(subject, "\r\n") {
		return errors.New("SMTP subject contains a newline")
	}
	message, err := buildMessage(s.from, recipient, subject, body)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return fmt.Errorf("SMTP connect: %w", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(sendTimeout)); err != nil {
		return fmt.Errorf("SMTP deadline: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	client, err := stdsmtp.NewClient(conn, s.host)
	if err != nil {
		return fmt.Errorf("SMTP greeting: %w", err)
	}
	defer client.Close()
	if err := client.Hello("localhost"); err != nil {
		return fmt.Errorf("SMTP hello: %w", err)
	}
	if err := client.Mail(s.from); err != nil {
		return fmt.Errorf("SMTP sender: %w", err)
	}
	if err := client.Rcpt(recipient); err != nil {
		return fmt.Errorf("SMTP recipient: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP data: %w", err)
	}
	if _, err := writer.Write(message); err != nil {
		_ = writer.Close()
		return fmt.Errorf("SMTP write: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("SMTP commit: %w", err)
	}
	// Closing DATA confirms acceptance. A lost QUIT response must not turn an
	// accepted message into a retryable failure.
	_ = client.Quit()
	return nil
}

func validAddress(address string) error {
	parsed, err := mail.ParseAddress(address)
	if err != nil || parsed.Address != address || strings.ContainsAny(address, "\r\n") {
		return errors.New("invalid email address")
	}
	return nil
}

func buildMessage(from, recipient, subject, body string) ([]byte, error) {
	return buildMessageWithID(from, recipient, subject, body, "")
}

func buildMessageWithID(from, recipient, subject, body, messageID string) ([]byte, error) {
	var message bytes.Buffer
	fmt.Fprintf(&message, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n", from, recipient, mime.QEncoding.Encode("utf-8", subject))
	if messageID != "" {
		// Insert before the blank line that separates headers from the body.
		data := message.Bytes()
		message.Reset()
		message.Write(data[:len(data)-2])
		fmt.Fprintf(&message, "Message-ID: %s\r\n\r\n", messageID)
	}
	content := strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	content = strings.ReplaceAll(content, "\n", "\r\n")
	writer := quotedprintable.NewWriter(&message)
	if _, err := writer.Write([]byte(content)); err != nil {
		return nil, fmt.Errorf("encode SMTP body: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finish SMTP body: %w", err)
	}
	return message.Bytes(), nil
}

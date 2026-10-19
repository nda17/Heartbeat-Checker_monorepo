package smtp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
)

const (
	productionTestHost = "mail.example.test"
	productionTestFrom = "alerts@example.test"
	productionTestTo   = "user@example.test"
	productionTestID   = "<job-123@heartbeat-checker.ru>"
)

type smtpTestServer struct {
	listener net.Listener
	cert     tls.Certificate
	mode     string
	replyAt  string
	code     int
	pauseAt  string
	paused   chan struct{}
	mu       sync.Mutex
	commands []string
	message  []byte
}

func newSMTPTestServer(t *testing.T, mode, replyAt string, code int, wrongName bool, pauseAt ...string) *smtpTestServer {
	t.Helper()
	cert := testCertificate(t, wrongName)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &smtpTestServer{listener: listener, cert: cert, mode: mode, replyAt: replyAt, code: code, paused: make(chan struct{})}
	if len(pauseAt) > 0 {
		s.pauseAt = pauseAt[0]
	}
	t.Cleanup(func() { _ = listener.Close() })
	go s.serve()
	return s
}

func testCertificate(t *testing.T, wrongName bool) tls.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	name := productionTestHost
	if wrongName {
		name = "wrong.example.test"
	}
	rootTemplate := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "test smtp root"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(42), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, rootTemplate, &leafKey.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	tlsCert := tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey, Leaf: leafTemplate}
	t.Setenv("HEARTBEAT_TEST_SMTP_ROOT", string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER})))
	return tlsCert
}

func (s *smtpTestServer) serve() {
	defer s.listener.Close()
	conn, err := s.listener.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	if s.mode == "implicit_tls" {
		conn = tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{s.cert}, MinVersion: tls.VersionTLS12})
		if err := conn.(*tls.Conn).Handshake(); err != nil {
			return
		}
	}
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	write := func(line string) bool {
		_, e := fmt.Fprintf(w, "%s\r\n", line)
		if e != nil {
			return false
		}
		return w.Flush() == nil
	}
	if !write("220 mail.example.test ESMTP ready") {
		return
	}
	secure := s.mode == "implicit_tls"
	for {
		line, e := r.ReadString('\n')
		if e != nil {
			return
		}
		cmd := strings.TrimSpace(line)
		s.mu.Lock()
		s.commands = append(s.commands, cmd)
		s.mu.Unlock()
		upper := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(upper, "EHLO ") || strings.HasPrefix(upper, "HELO "):
			if s.mode == "starttls" && !secure {
				if !write("250-mail.example.test") || !write("250-STARTTLS") || !write("250 AUTH PLAIN") {
					return
				}
			} else {
				if !write("250-mail.example.test") || !write("250 AUTH PLAIN") {
					return
				}
			}
		case upper == "STARTTLS":
			if !write("220 begin TLS") {
				return
			}
			tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{s.cert}, MinVersion: tls.VersionTLS12})
			if tlsConn.Handshake() != nil {
				return
			}
			conn = tlsConn
			r = bufio.NewReader(conn)
			w = bufio.NewWriter(conn)
			secure = true
		case strings.HasPrefix(upper, "AUTH "):
			if !secure {
				_ = write("530 authentication requires TLS")
				return
			}
			if !write("235 authenticated") {
				return
			}
		case strings.HasPrefix(upper, "MAIL FROM:"):
			if s.pauseAt == "MAIL" {
				close(s.paused)
				_, _ = io.Copy(io.Discard, conn)
				return
			}
			if s.replyAt == "MAIL" {
				_ = write(fmt.Sprintf("%d simulated failure", s.code))
				return
			}
			if !write("250 sender ok") {
				return
			}
		case strings.HasPrefix(upper, "RCPT TO:"):
			if s.replyAt == "RCPT" {
				_ = write(fmt.Sprintf("%d simulated failure", s.code))
				return
			}
			if !write("250 recipient ok") {
				return
			}
		case upper == "DATA":
			if !write("354 end with dot") {
				return
			}
			var msg bytes.Buffer
			for {
				dataLine, readErr := r.ReadString('\n')
				if readErr != nil {
					return
				}
				if dataLine == ".\r\n" {
					break
				}
				if strings.HasPrefix(dataLine, "..") {
					dataLine = dataLine[1:]
				}
				_, _ = msg.WriteString(dataLine)
			}
			s.mu.Lock()
			s.message = msg.Bytes()
			s.mu.Unlock()
			if !write("250 message accepted") {
				return
			}
		case upper == "QUIT":
			// Simulate the connection disappearing after DATA has been accepted.
			return
		default:
			if !write("500 unexpected command") {
				return
			}
		}
	}
}

func TestProductionSendWithIDHonorsEarlierContextDeadline(t *testing.T) {
	srv := newSMTPTestServer(t, "implicit_tls", "", 0, false, "MAIL")
	sender := srv.sender(t, productionTestHost)
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	started := time.Now()
	result := make(chan error, 1)
	go func() {
		result <- sender.SendWithID(ctx, productionTestTo, "Subject", contracts.MailContent{Text: "Body"}, productionTestID)
	}()
	select {
	case <-srv.paused:
	case <-time.After(time.Second):
		t.Fatal("SMTP server did not receive the message before deadline")
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("SendWithID() error = nil after context deadline")
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("SendWithID() ignored earlier context deadline for %v", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("SendWithID() did not stop when its context deadline expired")
	}
}

func (s *smtpTestServer) sender(t *testing.T, clientName string) *JobSender {
	t.Helper()
	rootPEM := strings.TrimSpace(getenv(t, "HEARTBEAT_TEST_SMTP_ROOT"))
	block, _ := pem.Decode([]byte(rootPEM))
	if block == nil {
		t.Fatal("test root certificate missing")
	}
	roots := x509.NewCertPool()
	root, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	roots.AddCert(root)
	return &JobSender{addr: s.listener.Addr().String(), host: clientName, from: productionTestFrom, username: "test-user", password: "test-password", mode: s.mode, tlsConfig: &tls.Config{ServerName: clientName, RootCAs: roots, MinVersion: tls.VersionTLS12}}
}

func getenv(t *testing.T, key string) string { t.Helper(); return os.Getenv(key) }

func TestProductionSendWithIDUsesVerifiedTLSAndSurvivesLostQUIT(t *testing.T) {
	for _, mode := range []string{"implicit_tls", "starttls"} {
		t.Run(mode, func(t *testing.T) {
			srv := newSMTPTestServer(t, mode, "", 0, false)
			err := srv.sender(t, productionTestHost).SendWithID(context.Background(), productionTestTo, "Verify your account", contracts.MailContent{Text: "Open the verification link"}, productionTestID)
			if err != nil {
				t.Fatalf("SendWithID() = %v", err)
			}
			srv.mu.Lock()
			defer srv.mu.Unlock()
			wire := string(srv.message)
			if !strings.Contains(wire, "Message-ID: "+productionTestID+"\r\n") || !strings.Contains(wire, "To: "+productionTestTo+"\r\n") {
				t.Fatalf("accepted message missing stable ID or recipient: %q", wire)
			}
			if strings.Contains(wire, "test-password") {
				t.Fatal("SMTP credentials leaked into message body")
			}
			var authIndex, tlsIndex int = -1, -1
			for i, cmd := range srv.commands {
				if strings.HasPrefix(strings.ToUpper(cmd), "AUTH ") {
					authIndex = i
				}
				if strings.EqualFold(cmd, "STARTTLS") {
					tlsIndex = i
				}
			}
			if mode == "starttls" && (tlsIndex < 0 || authIndex <= tlsIndex) {
				t.Fatalf("AUTH must follow STARTTLS: %v", srv.commands)
			}
		})
	}
}

func TestProductionSendWithIDRejectsUnverifiedHostnameBeforeSMTPAuth(t *testing.T) {
	srv := newSMTPTestServer(t, "implicit_tls", "", 0, true)
	err := srv.sender(t, productionTestHost).SendWithID(context.Background(), productionTestTo, "Subject", contracts.MailContent{Text: "Body"}, productionTestID)
	if err == nil {
		t.Fatal("SendWithID() error = nil for hostname mismatch")
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.commands) != 0 {
		t.Fatalf("SMTP commands sent before verified TLS: %v", srv.commands)
	}
}

func TestProductionSendWithIDClassifiesSMTP4xxAnd5xxAsRetryableOrPermanent(t *testing.T) {
	for _, code := range []int{450, 550} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			srv := newSMTPTestServer(t, "implicit_tls", "MAIL", code, false)
			err := srv.sender(t, productionTestHost).SendWithID(context.Background(), productionTestTo, "Subject", contracts.MailContent{Text: "Body"}, productionTestID)
			if err == nil {
				t.Fatal("SendWithID() error = nil for SMTP rejection")
			}
			var classified interface{ Permanent() bool }
			if !errorsAs(err, &classified) {
				t.Fatalf("error %T has no delivery classification", err)
			}
			if got, want := classified.Permanent(), code >= 500; got != want {
				t.Fatalf("Permanent() = %t, want %t", got, want)
			}
		})
	}
}

func errorsAs(err error, target any) bool { return errors.As(err, target) }

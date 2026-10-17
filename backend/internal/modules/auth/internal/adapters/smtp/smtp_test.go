package smtp

import (
	"io"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"testing"
)

func TestNewRestrictsSMTPToLoopbackAndValidFromAddress(t *testing.T) {
	tests := []struct{ addr, from string }{
		{"mail.example.com:25", "alerts@example.com"},
		{"0.0.0.0:1025", "alerts@example.com"},
		{"127.0.0.1:1025", "bad address"},
		{"127.0.0.1:1025", "alerts@example.com\r\nBcc: victim@example.com"},
	}
	for _, tt := range tests {
		if _, err := New(tt.addr, tt.from); err == nil {
			t.Errorf("New(%q, %q) error = nil, want validation error", tt.addr, tt.from)
		}
	}
	if _, err := New("[::1]:1025", "alerts@example.com"); err != nil {
		t.Fatalf("New() with IPv6 loopback: %v", err)
	}
}

func TestBuildMessageEncodesHeadersAndQuotedPrintableBody(t *testing.T) {
	message, err := buildMessage("Heartbeat <alerts@example.com>", "user@example.net", "Проверка ✓", "Состояние: работает\nСледующая строка.")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mail.ReadMessage(strings.NewReader(string(message)))
	if err != nil {
		t.Fatalf("ReadMessage(): %v", err)
	}
	from, err := mail.ParseAddress(parsed.Header.Get("From"))
	if err != nil || from.Address != "alerts@example.com" {
		t.Fatalf("From = %q, err = %v", parsed.Header.Get("From"), err)
	}
	to, err := mail.ParseAddress(parsed.Header.Get("To"))
	if err != nil || to.Address != "user@example.net" {
		t.Fatalf("To = %q, err = %v", parsed.Header.Get("To"), err)
	}
	decoder := mime.WordDecoder{}
	if got, err := decoder.DecodeHeader(parsed.Header.Get("Subject")); err != nil || got != "Проверка ✓" {
		t.Fatalf("decoded Subject = %q, err = %v", got, err)
	}
	if parsed.Header.Get("MIME-Version") != "1.0" || parsed.Header.Get("Content-Transfer-Encoding") != "quoted-printable" {
		t.Fatalf("missing MIME headers: %v", parsed.Header)
	}
	body, err := io.ReadAll(quotedprintable.NewReader(parsed.Body))
	if err != nil {
		t.Fatalf("decode quoted-printable body: %v", err)
	}
	if got, want := string(body), "Состояние: работает\r\nСледующая строка."; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestSendRejectsInvalidRecipientAndHeaderInjectionBeforeDial(t *testing.T) {
	sender, err := New("127.0.0.1:1", "alerts@example.com")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ recipient, subject string }{
		{"not-an-address", "subject"},
		{"user@example.com", "hello\r\nBcc: victim@example.com"},
	} {
		if err := sender.Send(t.Context(), tt.recipient, tt.subject, "body"); err == nil {
			t.Errorf("Send(%q, %q) error = nil, want validation error", tt.recipient, tt.subject)
		}
	}
}

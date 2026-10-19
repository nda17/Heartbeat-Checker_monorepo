package service

import (
	"strings"
	"testing"
	"time"
)

func TestRenderAuthMailEscapesUntrustedHTMLFieldsAndPreservesPlainText(t *testing.T) {
	subject, content, err := RenderAuthMail("oauth_registration", AuthMailData{
		Name: `<img src=x onerror="alert(1)">`, Provider: `<svg/onload=alert(1)>`,
		ActionURL: "https://heartbeat-checker.ru/verify-email?oauth=1#token=abc", ExpiresAt: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content.Text, `<img src=x onerror="alert(1)">`) || !strings.Contains(content.Text, `<svg/onload=alert(1)>`) {
		t.Fatalf("plain text unexpectedly altered: %q", content.Text)
	}
	if strings.Contains(content.HTML, `<img src=x`) || strings.Contains(content.HTML, `<svg/`) || !strings.Contains(content.HTML, "&lt;img") || !strings.Contains(content.HTML, "&lt;svg") {
		t.Fatalf("untrusted data was not safely escaped in HTML: %q", content.HTML)
	}
	if !strings.Contains(subject, "Heartbeat Checker") {
		t.Fatalf("subject=%q", subject)
	}
}

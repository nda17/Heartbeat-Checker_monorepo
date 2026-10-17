package turnstile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const (
	siteverifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
	dummyToken    = "XXXX.DUMMY.TOKEN.XXXX"
	maxResponse   = 8 << 10
)

var (
	ErrInvalid     = errors.New("turnstile verification failed")
	ErrUnavailable = errors.New("turnstile verification unavailable")
)

type Client struct {
	secret   string
	hostname string
	local    bool
	http     *http.Client
}

// New uses Cloudflare Siteverify in both modes. Local mode accepts only Cloudflare's
// published testing secrets and its dummy token; it never skips the network check.
func New(secret, hostname string, local bool) (*Client, error) {
	if secret == "" {
		return nil, errors.New("turnstile secret is required")
	}
	if local {
		if !isTestSecret(secret) {
			return nil, errors.New("local turnstile requires an official testing secret")
		}
	} else {
		if isTestSecret(secret) {
			return nil, errors.New("testing secret requires local turnstile mode")
		}
		if hostname == "" || strings.ContainsAny(hostname, "/:\r\n ") {
			return nil, errors.New("turnstile hostname is required")
		}
	}
	return &Client{
		secret:   secret,
		hostname: hostname,
		local:    local,
		http: &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (c *Client) Verify(ctx context.Context, token, remoteIP, action string) error {
	if token == "" || len(token) > 2048 || action == "" {
		return ErrInvalid
	}
	if c.local && token != dummyToken {
		return ErrInvalid
	}
	if remoteIP != "" {
		if _, err := netip.ParseAddr(remoteIP); err != nil {
			return ErrInvalid
		}
	}

	form := url.Values{"secret": {c.secret}, "response": {token}}
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, siteverifyURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("%w: create request", ErrUnavailable)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: request failed", ErrUnavailable)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: unexpected status %d", ErrUnavailable, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil || len(data) > maxResponse {
		return fmt.Errorf("%w: invalid response size", ErrUnavailable)
	}
	var result struct {
		Success    bool     `json:"success"`
		Hostname   string   `json:"hostname"`
		Action     string   `json:"action"`
		ErrorCodes []string `json:"error-codes"`
		Metadata   struct {
			TestingKey bool `json:"result_with_testing_key"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("%w: invalid response", ErrUnavailable)
	}
	if !result.Success {
		for _, code := range result.ErrorCodes {
			if code == "internal-error" || code == "invalid-input-secret" || code == "missing-input-secret" {
				return ErrUnavailable
			}
		}
		return ErrInvalid
	}
	if c.local {
		if !result.Metadata.TestingKey {
			return ErrInvalid
		}
		return nil
	}
	if !strings.EqualFold(result.Hostname, c.hostname) || result.Action != action {
		return ErrInvalid
	}
	return nil
}

func isTestSecret(secret string) bool {
	switch secret {
	case "1x0000000000000000000000000000000AA",
		"2x0000000000000000000000000000000AA",
		"3x0000000000000000000000000000000AA":
		return true
	default:
		return false
	}
}

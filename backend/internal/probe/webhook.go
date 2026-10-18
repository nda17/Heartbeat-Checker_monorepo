package probe

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/probe/contracts"
)

const maxWebhookBodyBytes = 16 << 10

func validateWebhookEnvelope(req contracts.WebhookRequest) error {
	if len(req.Body) == 0 || len(req.Body) > maxWebhookBodyBytes || !json.Valid(req.Body) || !validEventID(req.EventID) || len(req.Timestamp) == 0 || len(req.Timestamp) > 20 || len(req.Signature) != 67 || !strings.HasPrefix(req.Signature, "v1=") {
		return ErrUnsupportedTarget
	}
	for _, digit := range req.Timestamp {
		if digit < '0' || digit > '9' {
			return ErrUnsupportedTarget
		}
	}
	if _, err := strconv.ParseInt(req.Timestamp, 10, 64); err != nil {
		return ErrUnsupportedTarget
	}
	_, err := hex.DecodeString(req.Signature[3:])
	if err != nil {
		return ErrUnsupportedTarget
	}
	return nil
}

func validEventID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, ch := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if ch != '-' {
				return false
			}
			continue
		}
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') && (ch < 'A' || ch > 'F') {
			return false
		}
	}
	return true
}

func parseWebhookTarget(raw string) (*url.URL, error) {
	u, err := parseTarget(raw)
	if err != nil || u.Scheme != "https" || strings.HasSuffix(u.Host, ":") {
		return nil, ErrUnsupportedTarget
	}
	return u, nil
}

// DeliverWebhook sends one signed POST without redirects or post-send IP fallback.
func (e *Executor) DeliverWebhook(ctx context.Context, req contracts.WebhookRequest) (contracts.WebhookResult, error) {
	result := contracts.WebhookResult{EventID: req.EventID}
	if err := validateWebhookEnvelope(req); err != nil {
		return result, err
	}
	u, err := parseWebhookTarget(req.URL)
	if err != nil {
		result.ErrorCode = "blocked_target"
		return result, nil
	}
	select {
	case e.global <- struct{}{}:
		defer func() { <-e.global }()
	default:
		return result, ErrSaturated
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	response, err := e.webhookRequest(ctx, u, &req)
	if err != nil {
		if errors.Is(err, ErrSaturated) {
			return result, err
		}
		switch {
		case errors.Is(err, ErrUnsupportedTarget):
			result.ErrorCode = "blocked_target"
		case resourceFailure(err):
			result.ErrorCode = "probe_unavailable"
		default:
			result.ErrorCode = "request_failed"
		}
		return result, nil
	}
	result.HTTPStatus = response.StatusCode
	result.RetryAfterSeconds = retryAfterSeconds(response.Header.Get("Retry-After"), time.Now())
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result.ErrorCode = "http_status"
	}
	return result, nil
}

func (e *Executor) webhookRequest(ctx context.Context, u *url.URL, req *contracts.WebhookRequest) (*http.Response, error) {
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	releaseHost, err := e.acquire("host:" + host)
	if err != nil {
		return nil, err
	}
	defer releaseHost()
	ips, err := e.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	seen := make(map[netip.Addr]bool)
	var lastErr error
	saturated := false
	for _, ip := range ips {
		ip = ip.Unmap()
		if seen[ip] {
			continue
		}
		if len(seen) == 4 {
			break
		}
		seen[ip] = true
		response, _, err := e.requestIPWithPayload(ctx, u, http.MethodPost, ip, req)
		if errors.Is(err, ErrSaturated) {
			saturated = true
			continue
		}
		var establishmentErr *establishmentError
		if !errors.As(err, &establishmentErr) || ctx.Err() != nil || resourceFailure(err) {
			return response, err
		}
		lastErr = err
	}
	if saturated {
		return nil, ErrSaturated
	}
	return nil, lastErr
}

func retryAfterSeconds(raw string, now time.Time) int {
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		if seconds > 86400 {
			return 86400
		}
		return int(seconds)
	}
	date, err := http.ParseTime(raw)
	if err != nil {
		return 0
	}
	seconds := int(date.Sub(now).Seconds())
	if seconds <= 0 {
		return 0
	}
	if seconds > 86400 {
		return 86400
	}
	return seconds
}

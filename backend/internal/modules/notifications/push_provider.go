package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

type pushProviderError struct {
	code                                 string
	permanent, invalidToken, unavailable bool
	retryAfter                           time.Duration
}

func (e pushProviderError) Error() string { return "push provider unavailable" }

type RuStorePushSender struct {
	projectID, serviceToken string
	client                  *http.Client
}

func NewRuStorePushSender(projectID, serviceToken string) (*RuStorePushSender, error) {
	if projectID == "" || len(projectID) > 128 || strings.Trim(projectID, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") != "" || len(serviceToken) < 32 || strings.ContainsAny(serviceToken, " \t\r\n") {
		return nil, errors.New("RuStore push configuration is incomplete")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || host != "vkpns.rustore.ru" || port != "443" {
			return nil, errors.New("invalid push provider address")
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return nil, errors.New("push DNS unavailable")
		}
		for _, ip := range ips {
			if !messengerPublicIP(ip) {
				return nil, errors.New("unsafe push DNS")
			}
		}
		d := net.Dialer{Timeout: 10 * time.Second}
		for _, ip := range ips {
			conn, e := d.DialContext(ctx, network, net.JoinHostPort(netip.Addr(ip).String(), port))
			if e == nil {
				return conn, nil
			}
		}
		return nil, errors.New("push network unavailable")
	}
	return &RuStorePushSender{projectID: projectID, serviceToken: serviceToken, client: &http.Client{Timeout: 10 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// Send emits only an opaque delivery reference; notification content is fetched
// by the installation after the backend rechecks its source web session.
func (s *RuStorePushSender) Send(ctx context.Context, token string, data map[string]string, ttl time.Duration) error {
	seconds := int(ttl.Seconds())
	if seconds < 1 {
		return pushProviderError{code: "deadline_exceeded", permanent: true}
	}
	if seconds > 3600 {
		seconds = 3600
	}
	body, err := json.Marshal(map[string]any{"message": map[string]any{"token": token, "data": data, "android": map[string]string{"ttl": strconv.Itoa(seconds) + "s"}}})
	if err != nil || len(body) > 4096 {
		return pushProviderError{code: "invalid_argument", permanent: true}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://vkpns.rustore.ru/v1/projects/"+s.projectID+"/messages:send", bytes.NewReader(body))
	if err != nil {
		return pushProviderError{code: "request_failed"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.serviceToken)
	resp, err := s.client.Do(req)
	if err != nil {
		return pushProviderError{code: "request_failed"}
	}
	defer resp.Body.Close()
	response, err := io.ReadAll(io.LimitReader(resp.Body, 8193))
	if err != nil || len(response) > 8192 {
		return pushProviderError{code: "invalid_response"}
	}
	if resp.StatusCode == 200 {
		var accepted map[string]json.RawMessage
		if json.Unmarshal(response, &accepted) == nil && accepted != nil && len(accepted) == 0 {
			return nil
		}
		return pushProviderError{code: "invalid_response"}
	}
	var failure struct {
		Error struct {
			Status string `json:"status"`
		} `json:"error"`
	}
	_ = json.Unmarshal(response, &failure)
	result := pushProviderError{code: "provider_rejected", permanent: resp.StatusCode != 408 && resp.StatusCode != 429 && resp.StatusCode < 500}
	if seconds, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil && seconds > 0 && seconds <= 3600 {
		result.retryAfter = time.Duration(seconds) * time.Second
	}
	switch {
	case resp.StatusCode == 404 && (failure.Error.Status == "NOT_FOUND" || failure.Error.Status == "UNREGISTERED"):
		result.code = "invalid_token"
		result.invalidToken = true
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		result.code = "provider_unavailable"
		result.unavailable = true
	case resp.StatusCode == 400 && failure.Error.Status == "INVALID_ARGUMENT":
		result.code = "invalid_argument"
	case resp.StatusCode == 429:
		result.code = "rate_limited"
	case resp.StatusCode >= 500:
		result.code = "provider_unavailable"
	}
	return result
}

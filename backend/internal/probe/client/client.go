package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/probe/contracts"
)

type Client struct {
	endpoint        string
	webhookEndpoint string
	secret          string
	http            *http.Client
}

func New(endpoint, secret string) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(endpoint, "\r\n") || len(secret) < 32 || strings.TrimSpace(secret) != secret || strings.ContainsAny(secret, "\r\n") {
		return nil, errors.New("invalid probe configuration")
	}
	host := strings.ToLower(u.Hostname())
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		if ip.Zone() != "" || (!ip.IsLoopback() && !ip.IsPrivate()) {
			return nil, errors.New("probe endpoint must be internal")
		}
	} else if host != "localhost" && (strings.Contains(host, ".") || strings.ContainsAny(host, "%:")) {
		return nil, errors.New("probe endpoint must be internal")
	}
	u.Path = "/internal/v1/check"
	checkEndpoint := u.String()
	u.Path = "/internal/v1/webhook"
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Client{endpoint: checkEndpoint, webhookEndpoint: u.String(), secret: secret, http: &http.Client{Transport: transport, Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) Check(ctx context.Context, request contracts.Request) (contracts.Result, error) {
	var result contracts.Result
	body, err := json.Marshal(request)
	if err != nil || len(body) > 8<<10 {
		return result, errors.New("invalid probe request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return result, errors.New("invalid probe request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.secret)
	response, err := c.http.Do(req)
	if err != nil {
		return result, errors.New("probe unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return result, errors.New("probe check rejected")
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, (8<<10)+1))
	if err != nil || len(payload) > 8<<10 {
		return result, errors.New("invalid probe response")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return contracts.Result{}, errors.New("invalid probe response")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return contracts.Result{}, errors.New("invalid probe response")
	}
	if result.RunID != request.RunID || result.AttemptToken != request.AttemptToken || result.ConfigVersion != request.ConfigVersion || (result.Outcome != "UP" && result.Outcome != "DOWN" && result.Outcome != "UNKNOWN") || result.CheckedAt.IsZero() || result.CheckedAt.After(time.Now().Add(time.Minute)) || result.CheckedAt.Before(time.Now().Add(-time.Minute)) || result.LatencyMS < 0 || result.LatencyMS > 12000 || result.HTTPStatus < 0 || result.HTTPStatus > 599 || len(result.ErrorCode) > 64 {
		return contracts.Result{}, errors.New("invalid probe response")
	}
	if !contracts.ValidResult(request, result) {
		return contracts.Result{}, errors.New("invalid probe response")
	}
	return result, nil
}

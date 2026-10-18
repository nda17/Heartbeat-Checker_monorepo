package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/probe/contracts"
)

// DeliverWebhook asks the isolated probe to POST the exact signed body bytes.
func (c *Client) DeliverWebhook(ctx context.Context, request contracts.WebhookRequest) (contracts.WebhookResult, error) {
	var result contracts.WebhookResult
	if len(request.Body) == 0 || len(request.Body) > 16<<10 || !json.Valid(request.Body) {
		return result, errors.New("invalid webhook probe request")
	}
	body, err := json.Marshal(request)
	if err != nil || len(body) > 24<<10 {
		return result, errors.New("invalid webhook probe request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.webhookEndpoint, bytes.NewReader(body))
	if err != nil {
		return result, errors.New("invalid webhook probe request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.secret)
	response, err := c.http.Do(req)
	if err != nil {
		return result, errors.New("probe unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return result, errors.New("probe webhook rejected")
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, (8<<10)+1))
	if err != nil || len(payload) > 8<<10 {
		return result, errors.New("invalid probe response")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return contracts.WebhookResult{}, errors.New("invalid probe response")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return contracts.WebhookResult{}, errors.New("invalid probe response")
	}
	if result.EventID != request.EventID || result.HTTPStatus < 0 || result.HTTPStatus > 599 || (result.HTTPStatus > 0 && result.HTTPStatus < 100) || result.RetryAfterSeconds < 0 || result.RetryAfterSeconds > 86400 {
		return contracts.WebhookResult{}, errors.New("invalid probe response")
	}
	switch result.ErrorCode {
	case "":
		if result.HTTPStatus < 200 || result.HTTPStatus >= 300 {
			return contracts.WebhookResult{}, errors.New("invalid probe response")
		}
	case "http_status":
		if result.HTTPStatus >= 200 && result.HTTPStatus < 300 || result.HTTPStatus == 0 {
			return contracts.WebhookResult{}, errors.New("invalid probe response")
		}
	case "blocked_target", "request_failed", "probe_unavailable":
		if result.HTTPStatus != 0 || result.RetryAfterSeconds != 0 {
			return contracts.WebhookResult{}, errors.New("invalid probe response")
		}
	default:
		return contracts.WebhookResult{}, errors.New("invalid probe response")
	}
	return result, nil
}

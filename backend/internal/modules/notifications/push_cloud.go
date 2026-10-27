package notifications

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Provider clients accept no caller-controlled endpoints, redirects or proxies.
func pushHTTPClient(hosts ...string) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		allowed := false
		for _, candidate := range hosts {
			allowed = allowed || host == candidate
		}
		if err != nil || !allowed || port != "443" {
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
		dialer := net.Dialer{Timeout: 10 * time.Second}
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(netip.Addr(ip).String(), port))
			if err == nil {
				return conn, nil
			}
		}
		return nil, errors.New("push network unavailable")
	}
	return &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type cloudPushSender struct {
	provider, projectID, clientID, clientSecret, email string
	key                                                *rsa.PrivateKey
	client                                             *http.Client
	mu                                                 sync.Mutex
	accessToken                                        string
	expires                                            time.Time
}

// The service account belongs only to the worker; Android uses public options.
func NewFCMPushSender(projectID, encodedCredentials string) (PushSender, error) {
	data, err := base64.StdEncoding.Strict().DecodeString(encodedCredentials)
	if err != nil || len(data) > 32768 {
		return nil, errors.New("invalid FCM sending credentials")
	}
	var credentials struct {
		Type       string `json:"type"`
		ProjectID  string `json:"project_id"`
		Email      string `json:"client_email"`
		PrivateKey string `json:"private_key"`
		TokenURI   string `json:"token_uri"`
	}
	if json.Unmarshal(data, &credentials) != nil || credentials.Type != "service_account" || credentials.ProjectID != projectID || !strings.HasSuffix(credentials.Email, ".iam.gserviceaccount.com") || len(credentials.Email) > 256 || credentials.TokenURI != "https://oauth2.googleapis.com/token" {
		return nil, errors.New("invalid FCM sending credentials")
	}
	block, rest := pem.Decode([]byte(credentials.PrivateKey))
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("invalid FCM sending key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("invalid FCM sending key")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok || key.N.BitLen() < 2048 || key.Validate() != nil {
		return nil, errors.New("invalid FCM sending key")
	}
	return &cloudPushSender{provider: "fcm", projectID: projectID, email: credentials.Email, key: key, client: pushHTTPClient("fcm.googleapis.com", "oauth2.googleapis.com")}, nil
}

func NewHuaweiPushSender(projectID, clientID, clientSecret string) (PushSender, error) {
	if projectID == "" || strings.Trim(projectID, "0123456789") != "" || len(projectID) > 32 || clientID == "" || strings.Trim(clientID, "0123456789") != "" || len(clientID) > 32 || len(clientSecret) < 16 || len(clientSecret) > 8192 || strings.ContainsAny(clientSecret, " \t\r\n") {
		return nil, errors.New("invalid Huawei sending credentials")
	}
	return &cloudPushSender{provider: "huawei", projectID: projectID, clientID: clientID, clientSecret: clientSecret, client: pushHTTPClient("push-api.cloud.huawei.com", "oauth-login.cloud.huawei.com")}, nil
}

func (s *cloudPushSender) authorization(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.accessToken != "" && time.Until(s.expires) > time.Minute {
		return s.accessToken, nil
	}
	endpoint := "https://oauth-login.cloud.huawei.com/oauth2/v3/token"
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {s.clientID}, "client_secret": {s.clientSecret}}
	if s.provider == "fcm" {
		now := time.Now().Unix()
		claims, _ := json.Marshal(map[string]any{"iss": s.email, "scope": "https://www.googleapis.com/auth/firebase.messaging", "aud": "https://oauth2.googleapis.com/token", "iat": now, "exp": now + 3600})
		assertion := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims)
		digest := sha256.Sum256([]byte(assertion))
		signature, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, digest[:])
		if err != nil {
			return "", pushProviderError{code: "provider_unavailable", permanent: true, unavailable: true}
		}
		form = url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion + "." + base64.RawURLEncoding.EncodeToString(signature)}}
		endpoint = "https://oauth2.googleapis.com/token"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", pushProviderError{code: "request_failed"}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", pushProviderError{code: "request_failed"}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16385))
	if err != nil || len(raw) > 16384 {
		return "", pushProviderError{code: "invalid_response"}
	}
	if resp.StatusCode != 200 {
		return "", pushProviderError{code: "provider_unavailable", permanent: resp.StatusCode == 400 || resp.StatusCode == 401 || resp.StatusCode == 403, unavailable: resp.StatusCode == 400 || resp.StatusCode == 401 || resp.StatusCode == 403}
	}
	var result struct {
		AccessToken string `json:"access_token"`
		Expires     int64  `json:"expires_in"`
		Type        string `json:"token_type"`
	}
	if json.Unmarshal(raw, &result) != nil || len(result.AccessToken) < 16 || len(result.AccessToken) > 8192 || strings.ContainsAny(result.AccessToken, " \t\r\n") || result.Expires < 60 || result.Expires > 86400 || !strings.EqualFold(result.Type, "Bearer") {
		return "", pushProviderError{code: "invalid_response"}
	}
	s.accessToken, s.expires = result.AccessToken, time.Now().Add(time.Duration(result.Expires)*time.Second)
	return s.accessToken, nil
}

func (s *cloudPushSender) Send(ctx context.Context, destination string, data map[string]string, ttl time.Duration) error {
	seconds := min(int(ttl.Seconds()), 3600)
	if seconds < 1 {
		return pushProviderError{code: "deadline_exceeded", permanent: true}
	}
	token, err := s.authorization(ctx)
	if err != nil {
		return err
	}
	message := map[string]any{"fid": destination, "data": data, "android": map[string]any{"priority": "HIGH", "ttl": strconv.Itoa(seconds) + "s", "restricted_package_name": "ru.heartbeatchecker.app"}}
	endpoint := "https://fcm.googleapis.com/v1/projects/" + s.projectID + "/messages:send"
	if s.provider == "huawei" {
		opaque, e := json.Marshal(data)
		if e != nil {
			return pushProviderError{code: "invalid_argument", permanent: true}
		}
		message = map[string]any{"token": []string{destination}, "data": string(opaque), "android": map[string]any{"ttl": strconv.Itoa(seconds) + "s", "collapse_key": -1, "urgency": "HIGH"}}
		// Huawei's Android send API is scoped to the app-level OAuth client,
		// not the AppGallery Connect project identifier.
		endpoint = "https://push-api.cloud.huawei.com/v1/" + s.clientID + "/messages:send"
	}
	body, err := json.Marshal(map[string]any{"message": message})
	if err != nil || len(body) > 4096 {
		return pushProviderError{code: "invalid_argument", permanent: true}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return pushProviderError{code: "request_failed"}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return pushProviderError{code: "request_failed"}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8193))
	if err != nil || len(raw) > 8192 {
		return pushProviderError{code: "invalid_response"}
	}
	failure := pushProviderError{code: "provider_rejected", permanent: resp.StatusCode < 500 && resp.StatusCode != 408 && resp.StatusCode != 429}
	if retry, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil && retry > 0 && retry <= 3600 {
		failure.retryAfter = time.Duration(retry) * time.Second
	}
	if s.provider == "huawei" {
		var result struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(raw, &result) != nil {
			return pushProviderError{code: "invalid_response"}
		}
		if resp.StatusCode == 200 && result.Code == "80000000" {
			return nil
		}
		switch result.Code {
		case "80300007":
			failure.code, failure.invalidToken, failure.permanent = "invalid_token", true, true
		case "80200003":
			s.mu.Lock()
			s.accessToken = ""
			s.mu.Unlock()
			failure.code, failure.permanent = "authorization_expired", false
		case "80200001", "80300002", "80600003":
			failure.code, failure.unavailable, failure.permanent = "provider_unavailable", true, true
		case "81000001":
			failure.code, failure.permanent = "provider_unavailable", false
		}
	} else {
		var result struct {
			Name  string `json:"name"`
			Error struct {
				Status  string `json:"status"`
				Details []struct {
					Code string `json:"errorCode"`
				} `json:"details"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &result) != nil {
			return pushProviderError{code: "invalid_response"}
		}
		if resp.StatusCode == 200 && strings.HasPrefix(result.Name, "projects/"+s.projectID+"/messages/") {
			return nil
		}
		for _, detail := range result.Error.Details {
			if detail.Code == "UNREGISTERED" {
				failure.code, failure.invalidToken, failure.permanent = "invalid_token", true, true
			}
		}
	}
	if resp.StatusCode == 401 {
		s.mu.Lock()
		s.accessToken = ""
		s.mu.Unlock()
		failure.code, failure.permanent = "authorization_expired", false
	}
	if resp.StatusCode == 403 {
		failure.code, failure.unavailable, failure.permanent = "provider_unavailable", true, true
	}
	if resp.StatusCode == 429 {
		failure.code, failure.permanent = "rate_limited", false
	}
	return failure
}

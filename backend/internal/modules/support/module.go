// Package support routes dedicated customer support bots to operator chats within each provider.
package support

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Config struct {
	TelegramToken         string
	TelegramWebhookSecret string
	MAXToken              string
	MAXWebhookSecret      string
	EncryptionKey         string
	InternalChatID        int64
	SupportTopicID        int64
	MAXSupportChatID      int64
	TelegramAPIIP         string
	MAXRootCAFile         string
}

type Module struct {
	pool    *pgxpool.Pool
	cfg     Config
	cipher  cipher.AEAD
	client  *http.Client
	hashKey []byte
}

// NewWebhook constructs an ingest-only module. Bot tokens and outbound trust are not needed in API.
func NewWebhook(pool *pgxpool.Pool, cfg Config) (*Module, error) {
	return newModule(pool, cfg, false)
}

// New constructs the worker module, including isolated outbound provider transports.
func New(pool *pgxpool.Pool, cfg Config) (*Module, error) {
	return newModule(pool, cfg, true)
}

func newModule(pool *pgxpool.Pool, cfg Config, outbound bool) (*Module, error) {
	key, err := base64.StdEncoding.Strict().DecodeString(cfg.EncryptionKey)
	if err != nil || len(key) != 32 {
		return nil, errors.New("SUPPORT_ENCRYPTION_KEY must be base64 of 32 bytes")
	}
	if pool == nil || cfg.InternalChatID >= 0 || cfg.SupportTopicID <= 0 || (!outbound && (len(cfg.TelegramWebhookSecret) < 32 || len(cfg.MAXWebhookSecret) < 32)) {
		return nil, errors.New("support requires database, webhook secrets and internal topic")
	}
	if outbound && (cfg.MAXToken == "" || !strings.Contains(cfg.TelegramToken, ":") || strings.ContainsAny(cfg.TelegramToken, "/\r\n?#") || strings.ContainsAny(cfg.MAXToken, "\r\n")) {
		return nil, errors.New("invalid support bot token")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	box, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if !outbound {
		return &Module{pool: pool, cfg: cfg, cipher: box, hashKey: key}, nil
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = publicDial
	if cfg.TelegramAPIIP != "" {
		ip, err := netip.ParseAddr(cfg.TelegramAPIIP)
		if err != nil || !safeIP(ip) {
			return nil, errors.New("invalid TELEGRAM_API_PROXY_IP")
		}
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err == nil && host == "api.telegram.org" && port == "443" {
				d := net.Dialer{}
				return d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			}
			return publicDial(ctx, network, address)
		}
	}
	var maxTransport *http.Transport
	if cfg.MAXRootCAFile != "" {
		pem, err := os.ReadFile(cfg.MAXRootCAFile)
		if err != nil {
			return nil, errors.New("read support MAX root CA")
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, errors.New("read support certificate roots")
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("invalid support MAX root CA")
		}
		maxTransport = transport.Clone()
		maxTransport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	return &Module{pool: pool, cfg: cfg, cipher: box, hashKey: key, client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: providerTransport{standard: transport, max: maxTransport}}}, nil
}

// Register is intended for the ingest-only module created by NewWebhook.
func (m *Module) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /webhooks/support/telegram", m.webhook("telegram", m.cfg.TelegramWebhookSecret, "X-Telegram-Bot-Api-Secret-Token"))
	mux.HandleFunc("POST /webhooks/support/max", m.webhook("max", m.cfg.MAXWebhookSecret, "X-Max-Bot-Api-Secret"))
}

func (m *Module) webhook(provider, secret, header string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if len(secret) < 32 || subtle.ConstantTimeCompare([]byte(secret), []byte(r.Header.Get(header))) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "invalid body", http.StatusRequestEntityTooLarge)
			return
		}
		event, key, err := normalize(provider, body)
		if err != nil {
			http.Error(w, "invalid update", http.StatusBadRequest)
			return
		}
		if event == nil {
			w.WriteHeader(http.StatusOK)
			return
		}
		if event.Discovery == nil {
			if event.Bot || event.Anonymous || event.SenderID <= 0 {
				w.WriteHeader(http.StatusOK)
				return
			}
			if provider == "telegram" && !event.Private && (event.ChatID != m.cfg.InternalChatID || event.ThreadID != m.cfg.SupportTopicID || event.ReplyID == "") {
				w.WriteHeader(http.StatusOK)
				return
			}
			if provider == "max" && !event.Private && (m.cfg.MAXSupportChatID == 0 || event.ChatID != m.cfg.MAXSupportChatID) {
				// Once configured, unrelated group traffic has no discovery purpose.
				if m.cfg.MAXSupportChatID != 0 {
					w.WriteHeader(http.StatusOK)
					return
				}
				if event.Timestamp <= 0 {
					w.WriteHeader(http.StatusOK)
					return
				}
				event.Discovery = &Discovery{Type: "message_created", ChatID: event.ChatID, Timestamp: event.Timestamp}
				key = "discovery:message:" + key
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err = m.admit(ctx, provider, key, *event); err != nil {
			if errors.Is(err, errChangedDuplicate) {
				http.Error(w, "conflicting update", http.StatusConflict)
				return
			}
			if errors.Is(err, errAdmissionLimit) {
				w.Header().Set("Retry-After", "30")
				http.Error(w, "support queue busy", http.StatusTooManyRequests)
				return
			}
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func (m *Module) decode(provider, key string, payload []byte) (event, error) {
	var e event
	if len(payload) < m.cipher.NonceSize() {
		return e, errors.New("invalid support payload")
	}
	plain, err := m.cipher.Open(nil, payload[:m.cipher.NonceSize()], payload[m.cipher.NonceSize():], []byte(provider+":"+key))
	if err != nil {
		return e, errors.New("invalid support payload")
	}
	err = json.Unmarshal(plain, &e)
	return e, err
}

package support

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var errChangedDuplicate = errors.New("conflicting support update")
var errAdmissionLimit = errors.New("support admission limit")

type job struct {
	ID                         int64
	Provider, Key, Kind, Lease string
	Payload                    []byte
	Attempts                   int
	Created                    time.Time
}

type noticeRecipient struct {
	Provider string `json:"provider"`
	ChatID   int64  `json:"chatId"`
	SenderID int64  `json:"senderId"`
	Private  bool   `json:"private"`
}

func (m *Module) digest(value []byte) []byte {
	mac := hmac.New(sha256.New, m.hashKey)
	_, _ = mac.Write(value)
	return mac.Sum(nil)
}

func (m *Module) seal(provider, key string, plain []byte) ([]byte, error) {
	nonce := make([]byte, m.cipher.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return m.cipher.Seal(nonce, nonce, plain, []byte(provider+":"+key)), nil
}

// Admission and the duplicate check share one short database lock so multiple API
// replicas cannot race per-sender or global capacity. No provider I/O occurs here.
func (m *Module) admit(ctx context.Context, provider, key string, e event) error {
	kind, status := "message", "pending"
	var plain []byte
	var err error
	sender := fmt.Sprintf("%s:%d", provider, e.SenderID)
	if e.Discovery != nil {
		kind, status = "discovery", "recorded"
		// Keep one minimal diagnostic per chat/type, rather than one row per message.
		key = fmt.Sprintf("discovery:%s:%d", e.Discovery.Type, e.Discovery.ChatID)
		plain, err = json.Marshal(e.Discovery)
		sender = "discovery"
	} else if e.Start {
		if !e.Private || e.Bot || e.Anonymous || e.SenderID <= 0 || (provider == "telegram" && e.ChatID != e.SenderID) {
			return errRejected
		}
		kind = "welcome"
		key = "welcome:" + hex.EncodeToString(m.digest([]byte(sender)))
		plain, err = json.Marshal(noticeRecipient{Provider: provider, ChatID: e.SenderID, SenderID: e.SenderID, Private: true})
	} else {
		plain, err = json.Marshal(e)
	}
	if err != nil {
		return err
	}
	hash := m.digest(plain)
	senderKey := m.digest([]byte(sender))
	payload, err := m.seal(provider, key, plain)
	if err != nil {
		return err
	}
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7349281727)`); err != nil {
		return err
	}
	var previous, previousPayload []byte
	err = tx.QueryRow(ctx, `SELECT payload_hash,payload FROM support.inbox WHERE provider=$1 AND event_key=$2`, provider, key).Scan(&previous, &previousPayload)
	if err == nil {
		if kind == "discovery" && !hmac.Equal(hash, previous) {
			if len(previousPayload) < m.cipher.NonceSize() {
				return errors.New("invalid support discovery")
			}
			oldPlain, openErr := m.cipher.Open(nil, previousPayload[:m.cipher.NonceSize()], previousPayload[m.cipher.NonceSize():], []byte(provider+":"+key))
			if openErr != nil {
				return errors.New("invalid support discovery")
			}
			var old Discovery
			if json.Unmarshal(oldPlain, &old) != nil {
				return errors.New("invalid support discovery")
			}
			// Out-of-order webhook retries do not replace the latest observation.
			if old.Timestamp < e.Discovery.Timestamp {
				if _, err = tx.Exec(ctx, `UPDATE support.inbox SET payload=$3,payload_hash=$4,created_at=now(),finished_at=now() WHERE provider=$1 AND event_key=$2 AND event_kind='discovery'`, provider, key, payload, hash); err != nil {
					return err
				}
			}
			return tx.Commit(ctx)
		}
		if !hmac.Equal(hash, previous) {
			return errChangedDuplicate
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var bounded bool
	if kind == "discovery" {
		err = tx.QueryRow(ctx, `SELECT count(*)<1000 FROM support.inbox WHERE event_kind='discovery'`).Scan(&bounded)
	} else {
		err = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM support.inbox WHERE status IN ('pending','sending'))<10000 AND (SELECT count(*) FROM support.inbox WHERE provider=$1 AND status IN ('pending','sending'))<5000 AND (SELECT count(*) FROM support.inbox WHERE provider=$1 AND sender_key=$2 AND status IN ('pending','sending'))<50 AND (SELECT count(*) FROM support.inbox WHERE provider=$1 AND sender_key=$2 AND event_kind<>'ack' AND created_at>now()-interval '1 hour')<120`, provider, senderKey).Scan(&bounded)
	}
	if err != nil {
		return err
	}
	if !bounded {
		// Discovery is best effort. A full diagnostic buffer must not make the
		// provider retry unrelated lifecycle webhooks or crowd customer delivery.
		if kind == "discovery" {
			return tx.Commit(ctx)
		}
		return errAdmissionLimit
	}
	if _, err = tx.Exec(ctx, `INSERT INTO support.inbox(provider,event_key,event_kind,sender_key,payload_hash,payload,status,finished_at) VALUES($1,$2,$3,$4,$5,$6,$7,CASE WHEN $7='recorded' THEN now() ELSE NULL END)`, provider, key, kind, senderKey, hash, payload, status); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// The acknowledgement is committed with the forwarded message route. It is
// internal work and replaces the active source job, so it needs no admission cap.
func (m *Module) enqueueAck(ctx context.Context, tx pgx.Tx, j job, r route) error {
	key := fmt.Sprintf("ack:%d", j.ID)
	plain, err := json.Marshal(noticeRecipient{Provider: r.Provider, ChatID: r.UserID, SenderID: r.UserID, Private: true})
	if err != nil {
		return err
	}
	payload, err := m.seal(r.Provider, key, plain)
	if err != nil {
		return err
	}
	sender := fmt.Sprintf("%s:%d", r.Provider, r.UserID)
	_, err = tx.Exec(ctx, `INSERT INTO support.inbox(provider,event_key,event_kind,sender_key,payload_hash,payload) VALUES($1,$2,'ack',$3,$4,$5) ON CONFLICT(provider,event_key) DO NOTHING`, r.Provider, key, m.digest([]byte(sender)), m.digest(plain), payload)
	return err
}

// ListDiscovery is an explicit read-only diagnostic; it never binds or enables a chat.
func (m *Module) ListDiscovery(ctx context.Context) ([]Discovery, error) {
	rows, err := m.pool.Query(ctx, `SELECT provider,event_key,payload FROM support.inbox WHERE event_kind='discovery' AND created_at>now()-interval '7 days' ORDER BY id DESC LIMIT 1000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Discovery{}
	for rows.Next() {
		var provider, key string
		var payload []byte
		if err = rows.Scan(&provider, &key, &payload); err != nil {
			return nil, err
		}
		if len(payload) < m.cipher.NonceSize() {
			return nil, errors.New("invalid support discovery")
		}
		plain, err := m.cipher.Open(nil, payload[:m.cipher.NonceSize()], payload[m.cipher.NonceSize():], []byte(provider+":"+key))
		if err != nil {
			return nil, errors.New("invalid support discovery")
		}
		var d Discovery
		if json.Unmarshal(plain, &d) != nil {
			return nil, errors.New("invalid support discovery")
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

func (m *Module) claim(ctx context.Context) (*job, error) {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// All queued messages expire after 24 hours, including unconfigured MAX.
	// Separate provider admission caps preserve room for Telegram while MAX is paused.
	if _, err = tx.Exec(ctx, `UPDATE support.inbox SET status='failed',finished_at=now(),payload=decode('','hex'),lease_token=NULL,lease_until=NULL,last_error='attempt_limit' WHERE event_kind IN ('message','welcome','ack') AND (attempts>=8 OR created_at<=now()-interval '24 hours') AND (status='pending' OR (status='sending' AND lease_until<now()))`); err != nil {
		return nil, err
	}
	j := &job{}
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		return nil, err
	}
	j.Lease = hex.EncodeToString(token[:])
	err = tx.QueryRow(ctx, `SELECT id,provider,event_key,event_kind,payload,attempts,created_at FROM support.inbox WHERE event_kind IN ('message','welcome','ack') AND (provider<>'max' OR $1::bigint<>0) AND attempts<8 AND created_at>now()-interval '24 hours' AND next_attempt_at<=now() AND (status='pending' OR (status='sending' AND lease_until<now())) ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1`, m.cfg.MAXSupportChatID).Scan(&j.ID, &j.Provider, &j.Key, &j.Kind, &j.Payload, &j.Attempts, &j.Created)
	if errors.Is(err, pgx.ErrNoRows) {
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE support.inbox SET status='sending',attempts=attempts+1,first_attempt_at=COALESCE(first_attempt_at,now()),lease_token=$2,lease_until=now()+interval '2 minutes' WHERE id=$1`, j.ID, j.Lease); err != nil {
		return nil, err
	}
	j.Attempts++
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return j, nil
}

// ProcessOne deduplicates webhook updates. A lost provider response or a process
// crash after an external send can still cause a repeated delivery: these APIs
// do not accept an idempotency key for message sends.
func (m *Module) ProcessOne(ctx context.Context) (bool, error) {
	j, err := m.claim(ctx)
	if err != nil || j == nil {
		return false, err
	}
	sendCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	e, err := m.decode(j.Provider, j.Key, j.Payload)
	if err != nil {
		return true, m.finish(ctx, *j, "failed", "invalid_payload", 0, false)
	}
	if j.Kind == "welcome" || j.Kind == "ack" {
		err = m.processNotice(sendCtx, *j, e)
	} else {
		err = m.process(sendCtx, *j, e)
	}
	if err == nil {
		return true, m.finish(ctx, *j, "sent", "", 0, false)
	}
	var p *providerError
	if errors.As(err, &p) && p.permanent {
		return true, m.finish(ctx, *j, "failed", p.code, 0, false)
	}
	if errors.Is(err, errRejected) {
		return true, m.finish(ctx, *j, "failed", "rejected", 0, false)
	}
	if errors.Is(err, errMissingRoute) && time.Since(j.Created) > 2*time.Minute {
		return true, m.finish(ctx, *j, "failed", "unknown_reply", 0, false)
	}
	status, code := "pending", "provider_unavailable"
	if j.Attempts >= 8 {
		status = "failed"
	}
	var delay time.Duration
	noAttempt := false
	if p != nil {
		delay = p.retryAfter
		code = p.code
		if p.code == "target_rate" {
			noAttempt = true
			status = "pending"
		}
	}
	if errors.Is(err, errMissingRoute) {
		code = "route_pending"
	}
	if errors.Is(err, errMAXUnconfigured) {
		code = "max_unconfigured"
		noAttempt = true
		status = "pending"
		delay = time.Minute
	}
	return true, m.finish(ctx, *j, status, code, delay, noAttempt)
}

func (m *Module) finish(ctx context.Context, j job, status, code string, retryAfter time.Duration, noAttempt bool) error {
	delay := 5 * time.Second
	for i := 1; i < j.Attempts && delay < 30*time.Minute; i++ {
		delay *= 3
	}
	if delay > 30*time.Minute {
		delay = 30 * time.Minute
	}
	if noAttempt {
		delay = retryAfter
	} else if retryAfter > delay {
		delay = retryAfter
	}
	if delay < time.Millisecond {
		delay = time.Millisecond
	}
	_, err := m.pool.Exec(ctx, `UPDATE support.inbox SET status=$3,last_error=NULLIF($4,''),next_attempt_at=now()+$5::interval,finished_at=CASE WHEN $3 IN ('sent','failed') THEN now() ELSE NULL END,payload=CASE WHEN $3 IN ('sent','failed') THEN decode('','hex') ELSE payload END,attempts=attempts-CASE WHEN $6 THEN 1 ELSE 0 END,lease_token=NULL,lease_until=NULL WHERE id=$1 AND status='sending' AND lease_token=$2`, j.ID, j.Lease, status, code, delay.String(), noAttempt)
	return err
}

func (m *Module) reserveTarget(ctx context.Context, provider, address string, interval time.Duration) error {
	tag, err := m.pool.Exec(ctx, `INSERT INTO support.target_rates(provider,target_key,next_send_at) VALUES($1,$2,now()+$3::interval) ON CONFLICT(provider,target_key) DO UPDATE SET next_send_at=now()+$3::interval WHERE support.target_rates.next_send_at<=now()`, provider, m.digest([]byte(address)), interval.String())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return &providerError{code: "target_rate", retryAfter: interval}
	}
	return nil
}

func (m *Module) Cleanup(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `DELETE FROM support.inbox WHERE id IN (SELECT id FROM support.inbox WHERE (event_kind='discovery' AND created_at<now()-interval '7 days') OR (event_kind IN ('message','ack') AND finished_at<now()-interval '30 days') ORDER BY id LIMIT 1000)`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM support.message_routes WHERE (provider,operator_chat_id,operator_message_id) IN (SELECT provider,operator_chat_id,operator_message_id FROM support.message_routes WHERE created_at<now()-interval '90 days' ORDER BY created_at LIMIT 1000)`); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM support.target_rates WHERE (provider,target_key) IN (SELECT provider,target_key FROM support.target_rates WHERE next_send_at<now()-interval '1 day' LIMIT 1000)`)
	return err
}

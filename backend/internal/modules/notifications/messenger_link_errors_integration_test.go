package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func postTelegramLinkUpdate(t *testing.T, m *Messenger, updateID int, text, chatType string, chatID, userID, threadID int64) {
	t.Helper()
	mux := http.NewServeMux()
	m.RegisterIngest(mux)
	body := fmt.Sprintf(`{"update_id":%d,"message":{"text":%q,"message_thread_id":%d,"chat":{"id":%d,"type":%q},"from":{"id":%d,"is_bot":false}}}`, updateID, text, threadID, chatID, chatType, userID)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/notifications/telegram", strings.NewReader(body))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "test-telegram-webhook-secret-32-bytes")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Telegram link update status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func observedTelegramLink(t *testing.T, f *webhookFixture, m *Messenger, accountID, kind string, chatID, userID int64, updateID int) MessengerLink {
	t.Helper()
	link, token, _, command, err := m.CreateLink(context.Background(), accountID, "telegram", kind)
	if err != nil {
		t.Fatalf("create Telegram %s link: %v", kind, err)
	}
	text, chatType := "/start "+token, "private"
	var threadID int64
	if kind != "private" {
		text, chatType = command, "supergroup"
	}
	if kind == "topic" {
		threadID = 44
	}
	postTelegramLinkUpdate(t, m, updateID, text, chatType, chatID, userID, threadID)
	return link
}

func linkState(t *testing.T, f *webhookFixture, id string) (status, code string, attempts int, expires time.Time, token, lease *string) {
	t.Helper()
	err := f.pool.QueryRow(context.Background(), `SELECT status,error_code,attempts,expires_at,attempt_token::text,lease_until::text FROM notifications.messenger_links WHERE id=$1`, id).Scan(&status, &code, &attempts, &expires, &token, &lease)
	if err != nil {
		t.Fatalf("read Telegram link state: %v", err)
	}
	return
}

func TestMessengerLinkPermanentTelegramPermissionFailuresAreSpecificAndRestartable(t *testing.T) {
	for _, kind := range []string{"group", "topic"} {
		for _, permission := range []struct{ name, deniedUser, expectedCode string }{
			{name: "bot is only a member", deniedUser: "bot", expectedCode: "telegram_bot_admin_required"},
			{name: "linking user is only a member", deniedUser: "user", expectedCode: "telegram_user_admin_required"},
		} {
			t.Run(kind+" / "+permission.name, func(t *testing.T) {
				f := newWebhookFixture(t)
				accountID, _ := f.addMonitor(t, kind+" "+permission.name)
				m := testMessenger(t, f, true)
				confirmPrivateTelegramTarget(t, f, m, accountID, 321)
				link := observedTelegramLink(t, f, m, accountID, kind, -10077, 321, 77)
				m.client = &http.Client{Transport: messengerRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					method := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
					switch method {
					case "getMe":
						return messengerJSONResponse(200, `{"ok":true,"result":{"id":700,"username":"heartbeat_test_bot","is_bot":true}}`), nil
					case "getChat":
						return messengerJSONResponse(200, fmt.Sprintf(`{"ok":true,"result":{"id":-10077,"type":"supergroup","title":"Operations","is_forum":%t}}`, kind == "topic")), nil
					case "getChatMember":
						var input struct {
							UserID int64 `json:"user_id"`
						}
						if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
							return nil, err
						}
						status := "administrator"
						if permission.deniedUser == "bot" && input.UserID == 700 || permission.deniedUser == "user" && input.UserID == 321 {
							status = "member"
						}
						return messengerJSONResponse(200, fmt.Sprintf(`{"ok":true,"result":{"status":%q,"user":{"id":%d}}}`, status, input.UserID)), nil
					default:
						return nil, fmt.Errorf("unexpected Telegram verification request %s", req.URL)
					}
				})}
				if processed, err := m.ProcessLink(context.Background()); err != nil || !processed {
					t.Fatalf("process permission failure processed=%t err=%v", processed, err)
				}
				status, code, attempts, expires, token, lease := linkState(t, f, link.ID)
				if status != "failed" || code != permission.expectedCode || attempts != 1 || !expires.After(time.Now()) || token != nil || lease != nil {
					t.Fatalf("link status=%q code=%q attempts=%d expires=%s token=%v lease=%v; want permanent failure before TTL with a cleared lease", status, code, attempts, expires, token, lease)
				}
			})
		}
	}
}

func TestMessengerGroupAndTopicCommandsMustMatchOwnersPrivateTelegramUser(t *testing.T) {
	for _, kind := range []string{"group", "topic"} {
		t.Run(kind, func(t *testing.T) {
			f := newWebhookFixture(t)
			accountID, _ := f.addMonitor(t, "mismatched Telegram "+kind)
			m := testMessenger(t, f, true)
			confirmPrivateTelegramTarget(t, f, m, accountID, 654)
			link := observedTelegramLink(t, f, m, accountID, kind, -10078, 321, 78)
			providerCalls := 0
			m.client = &http.Client{Transport: messengerRoundTripFunc(func(*http.Request) (*http.Response, error) {
				providerCalls++
				return nil, fmt.Errorf("provider verification must not run for an unbound user")
			})}
			if processed, err := m.ProcessLink(context.Background()); err != nil || !processed {
				t.Fatalf("process mismatched user link processed=%t err=%v", processed, err)
			}
			status, code, attempts, expires, token, lease := linkState(t, f, link.ID)
			if status != "failed" || code != "telegram_private_required" || attempts != 1 || !expires.After(time.Now()) || token != nil || lease != nil || providerCalls != 0 {
				t.Fatalf("mismatched user state=%s/%s attempts=%d providerCalls=%d token=%v lease=%v", status, code, attempts, providerCalls, token, lease)
			}

			candidate, _, _, _, err := m.CreateLink(context.Background(), accountID, "telegram", kind)
			if err != nil {
				t.Fatal(err)
			}
			target := messengerTarget{Provider: "telegram", Kind: kind, ChatID: "-10079", UserID: "321"}
			if kind == "topic" {
				target.ThreadID = "44"
			}
			plain, err := json.Marshal(messengerRecipient{Target: target, Label: "Operations"})
			if err != nil {
				t.Fatal(err)
			}
			encrypted, err := m.seal(accountID, candidate.ID, "candidate", plain)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.pool.Exec(context.Background(), `UPDATE notifications.messenger_links SET status='candidate',encrypted_candidate=$2 WHERE id=$1`, candidate.ID, encrypted); err != nil {
				t.Fatal(err)
			}
			if _, err := m.ConfirmLink(context.Background(), accountID, candidate.ID); !errors.Is(err, ErrTelegramPrivateRequired) {
				t.Fatalf("confirm candidate from unbound Telegram user error=%v, want same private-chat requirement", err)
			}
		})
	}
}

func TestMessengerLinkProviderRetryBecomesSpecificFailureOnlyAtExhaustion(t *testing.T) {
	f := newWebhookFixture(t)
	accountID, _ := f.addMonitor(t, "Telegram verification retries")
	m := testMessenger(t, f, true)
	link := observedTelegramLink(t, f, m, accountID, "private", 321, 321, 78)
	m.client = &http.Client{Transport: messengerRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return messengerJSONResponse(503, `{"ok":false,"description":"temporary provider outage"}`), nil
	})}
	if processed, err := m.ProcessLink(context.Background()); err != nil || !processed {
		t.Fatalf("first retryable attempt processed=%t err=%v", processed, err)
	}
	status, code, attempts, expires, token, lease := linkState(t, f, link.ID)
	if status != "observed" || code != "" || attempts != 1 || !expires.After(time.Now()) || token != nil || lease != nil {
		t.Fatalf("retry state status=%q code=%q attempts=%d expires=%s token=%v lease=%v; want observed and retryable", status, code, attempts, expires, token, lease)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.messenger_links SET attempts=7,next_attempt_at=now()-interval '1 second' WHERE id=$1`, link.ID); err != nil {
		t.Fatal(err)
	}
	if processed, err := m.ProcessLink(context.Background()); err != nil || !processed {
		t.Fatalf("exhausting retry attempt processed=%t err=%v", processed, err)
	}
	status, code, attempts, expires, token, lease = linkState(t, f, link.ID)
	if status != "failed" || code != "verification_unavailable" || attempts != 8 || !expires.After(time.Now()) || token != nil || lease != nil {
		t.Fatalf("exhausted state status=%q code=%q attempts=%d expires=%s token=%v lease=%v; want bounded terminal failure before TTL", status, code, attempts, expires, token, lease)
	}
}

func TestMessengerLinkExpiredStatusUsesTTLAndLegacyFailureStaysUnknown(t *testing.T) {
	f := newWebhookFixture(t)
	accountID, _ := f.addMonitor(t, "Telegram link expiry")
	m := testMessenger(t, f, true)
	for _, state := range []string{"pending", "observed", "candidate"} {
		link, _, _, _, err := m.CreateLink(context.Background(), accountID, "telegram", "private")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.messenger_links SET status=$2,error_code='',encrypted_candidate=CASE WHEN $2='candidate' THEN decode('00','hex') ELSE NULL END,expires_at=now()-interval '1 second' WHERE id=$1`, link.ID, state); err != nil {
			t.Fatal(err)
		}
		got, err := m.GetLink(context.Background(), accountID, link.ID)
		if err != nil || got.Status != "expired" || got.ErrorCode != "link_expired" {
			t.Fatalf("expired %s link=%+v errorCode=%q err=%v", state, got, got.ErrorCode, err)
		}
		if state == "candidate" {
			if _, err := m.ConfirmLink(context.Background(), accountID, link.ID); err == nil {
				t.Fatal("expired candidate was confirmed")
			}
		}
	}
	legacy, _, _, _, err := m.CreateLink(context.Background(), accountID, "telegram", "private")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.messenger_links SET status='expired',error_code='' WHERE id=$1`, legacy.ID); err != nil {
		t.Fatal(err)
	}
	got, err := m.GetLink(context.Background(), accountID, legacy.ID)
	if err != nil || got.Status != "expired" || got.ErrorCode != "" {
		t.Fatalf("legacy expired link=%+v errorCode=%q err=%v; do not infer a reason for migrated legacy data", got, got.ErrorCode, err)
	}
}

func TestMessengerLinkCancellationIsIdempotentOwnerScopedAndProtectsConfirmedLinks(t *testing.T) {
	f := newMessengerAPIFixture(t, true, "cancel link owner", "cancel link foreign")
	owner := f.actors["cancel link owner"]
	foreign := f.actors["cancel link foreign"]
	create := func() MessengerLink {
		t.Helper()
		link, _, _, _, err := f.module.CreateLink(context.Background(), owner.accountID, "telegram", "private")
		if err != nil {
			t.Fatalf("create isolated cancellation link: %v", err)
		}
		return link
	}

	link := create()
	path := "/api/v1/messenger-links/" + link.ID
	if got := f.request(t, &foreign, http.MethodDelete, path, nil, foreign.csrf, messengerTestOrigin); got.Code != http.StatusNotFound {
		t.Fatalf("foreign cancellation status=%d body=%s, want 404", got.Code, got.Body.String())
	}
	for attempt := 1; attempt <= 2; attempt++ {
		got := f.request(t, &owner, http.MethodDelete, path, nil, owner.csrf, messengerTestOrigin)
		if got.Code != http.StatusNoContent || got.Body.Len() != 0 {
			t.Fatalf("owner cancellation attempt %d status=%d body=%s, want empty 204", attempt, got.Code, got.Body.String())
		}
	}
	status, code, attempts, _, token, lease := linkState(t, f.webhookFixture, link.ID)
	var observation, candidate []byte
	if err := f.webhookFixture.pool.QueryRow(context.Background(), `SELECT encrypted_observation,encrypted_candidate FROM notifications.messenger_links WHERE id=$1`, link.ID).Scan(&observation, &candidate); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || code != "link_cancelled" || attempts != 0 || token != nil || lease != nil || observation != nil || candidate != nil {
		t.Fatalf("cancelled link state=%s/%s attempts=%d token=%v lease=%v observation=%v candidate=%v", status, code, attempts, token, lease, observation, candidate)
	}

	expired := create()
	if _, err := f.webhookFixture.pool.Exec(context.Background(), `UPDATE notifications.messenger_links SET status='expired',error_code='link_expired',expires_at=now()-interval '1 second' WHERE id=$1`, expired.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.request(t, &owner, http.MethodDelete, "/api/v1/messenger-links/"+expired.ID, nil, owner.csrf, messengerTestOrigin); got.Code != http.StatusNoContent {
		t.Fatalf("repeated cancellation of expired attempt status=%d body=%s, want 204", got.Code, got.Body.String())
	}
	candidateLink := create()
	if _, err := f.webhookFixture.pool.Exec(context.Background(), `UPDATE notifications.messenger_links SET status='candidate',encrypted_candidate=decode('00','hex') WHERE id=$1`, candidateLink.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.request(t, &owner, http.MethodDelete, "/api/v1/messenger-links/"+candidateLink.ID, nil, owner.csrf, messengerTestOrigin); got.Code != http.StatusNoContent {
		t.Fatalf("candidate cancellation status=%d body=%s, want 204", got.Code, got.Body.String())
	}
	candidateStatus, candidateCode, _, _, _, _ := linkState(t, f.webhookFixture, candidateLink.ID)
	var candidateCipher []byte
	if err := f.webhookFixture.pool.QueryRow(context.Background(), `SELECT encrypted_candidate FROM notifications.messenger_links WHERE id=$1`, candidateLink.ID).Scan(&candidateCipher); err != nil {
		t.Fatal(err)
	}
	if candidateStatus != "failed" || candidateCode != "link_cancelled" || candidateCipher != nil {
		t.Fatalf("cancelled candidate status=%s/%s still retains ciphertext: %v", candidateStatus, candidateCode, candidateCipher)
	}

	destination := confirmPrivateTelegramTarget(t, f.webhookFixture, f.module, owner.accountID, 654)
	confirmed := create()
	if _, err := f.webhookFixture.pool.Exec(context.Background(), `UPDATE notifications.messenger_links SET status='confirmed',destination_id=$2 WHERE id=$1`, confirmed.ID, destination.ID); err != nil {
		t.Fatal(err)
	}
	var beforeDestinations int
	if err := f.webhookFixture.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications.messenger_destinations WHERE account_id=$1`, owner.accountID).Scan(&beforeDestinations); err != nil {
		t.Fatal(err)
	}
	denied := f.request(t, &owner, http.MethodDelete, "/api/v1/messenger-links/"+confirmed.ID, nil, owner.csrf, messengerTestOrigin)
	if denied.Code != http.StatusConflict || !strings.Contains(denied.Body.String(), "link_already_confirmed") {
		t.Fatalf("confirmed link cancellation status=%d body=%s, want 409 link_already_confirmed", denied.Code, denied.Body.String())
	}
	var afterDestinations int
	if err := f.webhookFixture.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications.messenger_destinations WHERE account_id=$1`, owner.accountID).Scan(&afterDestinations); err != nil {
		t.Fatal(err)
	}
	got, err := f.module.GetLink(context.Background(), owner.accountID, confirmed.ID)
	if err != nil || got.Status != "confirmed" || got.DestinationID == nil || *got.DestinationID != destination.ID || afterDestinations != beforeDestinations {
		t.Fatalf("confirmed link/destination changed after rejected cancellation: link=%+v destinationCount=%d->%d err=%v", got, beforeDestinations, afterDestinations, err)
	}
}

func TestMessengerLinkCancellationWinsAgainstInFlightVerificationFinish(t *testing.T) {
	f := newWebhookFixture(t)
	accountID, _ := f.addMonitor(t, "cancel link during verification")
	m := testMessenger(t, f, true)
	link := observedTelegramLink(t, f, m, accountID, "private", 321, 321, 801)
	entered, release := make(chan struct{}), make(chan struct{})
	m.client = &http.Client{Transport: messengerRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.URL.Path, "/getMe") {
			return nil, fmt.Errorf("unexpected Telegram verification request %s", req.URL)
		}
		close(entered)
		<-release
		return messengerJSONResponse(200, `{"ok":true,"result":{"id":700,"username":"heartbeat_test_bot","is_bot":true}}`), nil
	})}
	type result struct {
		processed bool
		err       error
	}
	done := make(chan result, 1)
	go func() {
		processed, err := m.ProcessLink(context.Background())
		done <- result{processed: processed, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("verification worker did not reach the synthetic provider")
	}
	status, code, attempts, _, attemptToken, lease := linkState(t, f, link.ID)
	if status != "observed" || attempts != 1 || attemptToken == nil || lease == nil {
		close(release)
		t.Fatalf("expected claimed observed link before cancellation, state=%s/%s attempts=%d token=%v lease=%v", status, code, attempts, attemptToken, lease)
	}
	if err := m.CancelLink(context.Background(), accountID, link.ID); err != nil {
		close(release)
		t.Fatalf("cancel while verifier is in flight: %v", err)
	}
	close(release)
	select {
	case result := <-done:
		if result.err != nil || !result.processed {
			t.Fatalf("in-flight verifier result=(processed=%t, err=%v)", result.processed, result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight verifier did not finish after release")
	}
	status, code, attempts, _, attemptToken, lease = linkState(t, f, link.ID)
	var observation, candidate []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT encrypted_observation,encrypted_candidate FROM notifications.messenger_links WHERE id=$1`, link.ID).Scan(&observation, &candidate); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || code != "link_cancelled" || attempts != 1 || attemptToken != nil || lease != nil || observation != nil || candidate != nil {
		t.Fatalf("late finish resurrected cancelled link: state=%s/%s attempts=%d token=%v lease=%v observation=%v candidate=%v", status, code, attempts, attemptToken, lease, observation, candidate)
	}
}

func TestMessengerGroupLinkCreationRequiresOwnersReadyPrivateChat(t *testing.T) {
	f := newMessengerAPIFixture(t, true, "group owner without private", "another account with private")
	owner := f.actors["group owner without private"]
	other := f.actors["another account with private"]
	confirmPrivateTelegramTarget(t, f.webhookFixture, f.module, other.accountID, 654)
	for _, kind := range []string{"group", "topic"} {
		for _, testCase := range []struct{ name, accountID string }{
			{name: "no ready private chat", accountID: owner.accountID},
			{name: "another account has a private chat", accountID: owner.accountID},
		} {
			t.Run(kind+" / "+testCase.name, func(t *testing.T) {
				before := 0
				if err := f.webhookFixture.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications.messenger_links WHERE account_id=$1`, testCase.accountID).Scan(&before); err != nil {
					t.Fatal(err)
				}
				rec := f.request(t, &owner, http.MethodPost, "/api/v1/messenger-links", map[string]string{"provider": "telegram", "kind": kind}, owner.csrf, messengerTestOrigin)
				var body struct {
					Error string `json:"error"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if rec.Code != http.StatusConflict || body.Error != "telegram_private_required" {
					t.Fatalf("group create status=%d body=%s, want 409 telegram_private_required", rec.Code, rec.Body.String())
				}
				after := 0
				if err := f.webhookFixture.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications.messenger_links WHERE account_id=$1`, testCase.accountID).Scan(&after); err != nil {
					t.Fatal(err)
				}
				if after != before {
					t.Fatalf("rejected group create inserted a link: before=%d after=%d", before, after)
				}
			})
		}
	}
}

func TestMessengerLinkOldAttemptCannotClearOrReplaceCurrentLease(t *testing.T) {
	f := newWebhookFixture(t)
	accountID, _ := f.addMonitor(t, "Telegram stale verification lease")
	otherAccountID, _ := f.addMonitor(t, "foreign Telegram link reader")
	m := testMessenger(t, f, true)
	link := observedTelegramLink(t, f, m, accountID, "private", 321, 321, 79)
	entered, release := make(chan struct{}), make(chan struct{})
	m.client = &http.Client{Transport: messengerRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.URL.Path, "/getMe") {
			return nil, fmt.Errorf("unexpected Telegram verification request %s", req.URL)
		}
		close(entered)
		<-release
		return messengerJSONResponse(200, `{"ok":true,"result":{"id":700,"username":"heartbeat_test_bot","is_bot":true}}`), nil
	})}
	if _, err := m.GetLink(context.Background(), otherAccountID, link.ID); !errors.Is(err, ErrMessengerNotFound) {
		t.Fatalf("foreign account read error=%v, want not found", err)
	}
	done := make(chan error, 1)
	go func() { _, err := m.ProcessLink(context.Background()); done <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("verification provider call did not start")
	}
	const replacementToken = "60000000-0000-4000-8000-000000000001"
	if _, err := f.pool.Exec(context.Background(), `UPDATE notifications.messenger_links SET attempt_token=$2,lease_until=now()+interval '1 minute' WHERE id=$1`, link.ID, replacementToken); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("stale verification returned error: %v", err)
	}
	status, code, attempts, expires, token, lease := linkState(t, f, link.ID)
	var owner string
	if err := f.pool.QueryRow(context.Background(), `SELECT account_id::text FROM notifications.messenger_links WHERE id=$1`, link.ID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != accountID || status != "observed" || code != "" || attempts != 1 || !expires.After(time.Now()) || token == nil || *token != replacementToken || lease == nil {
		t.Fatalf("stale finish changed owner or active lease: owner=%s status=%q code=%q attempts=%d token=%v lease=%v", owner, status, code, attempts, token, lease)
	}
}

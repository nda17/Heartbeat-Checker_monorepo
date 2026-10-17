.PHONY: db-up mail-up local-up db-down migrate run check test-integration test-live-auth test-smtp-auth

DATABASE_URL ?= postgres://heartbeat_local:heartbeat_local_only@127.0.0.1:55432/heartbeat_local?sslmode=disable
export DATABASE_URL

db-up:
	docker compose -f deploy/local/compose.yaml up -d --wait postgres

mail-up:
	docker compose -f deploy/local/compose.yaml up -d --wait mailpit

local-up:
	docker compose -f deploy/local/compose.yaml up -d --wait

db-down:
	docker compose -f deploy/local/compose.yaml down

migrate:
	cd backend && go run ./cmd/migrate

run:
	cd backend && AUTH_ORIGIN="$${AUTH_ORIGIN:-http://127.0.0.1:8080}" \
		AUTH_COOKIE_SECURE="$${AUTH_COOKIE_SECURE:-false}" \
		TURNSTILE_LOCAL_TEST_MODE="$${TURNSTILE_LOCAL_TEST_MODE:-true}" \
		TURNSTILE_SECRET_KEY="$${TURNSTILE_SECRET_KEY:-1x0000000000000000000000000000000AA}" \
		TURNSTILE_HOSTNAME="$${TURNSTILE_HOSTNAME:-localhost}" \
		SMTP_ADDR="$${SMTP_ADDR:-127.0.0.1:51025}" \
		SMTP_FROM="$${SMTP_FROM:-no-reply@heartbeat.local}" \
		go run ./cmd/api

check:
	cd backend && test -z "$$(gofmt -l .)"
	cd backend && go vet ./...
	cd backend && go test ./...
	cd backend && go build -o /dev/null ./cmd/api
	cd backend && go build -o /dev/null ./cmd/migrate

test-integration: db-up
	cd backend && HEARTBEAT_TEST_DATABASE_URL="$${DATABASE_URL}" go test -count=1 ./migrations ./internal/modules/auth

test-live-auth: local-up
	cd backend && HEARTBEAT_LIVE_AUTH=true HEARTBEAT_TEST_DATABASE_URL="$${DATABASE_URL}" go test -count=1 ./internal/modules/auth -run '^TestLiveAuthProviderSmoke$$'

test-smtp-auth: local-up
	cd backend && HEARTBEAT_LIVE_AUTH=true HEARTBEAT_TEST_DATABASE_URL="$${DATABASE_URL}" go test -count=1 ./internal/modules/auth -run '^TestLocalSMTPAuthSmoke$$'

.PHONY: db-up mail-up local-up db-down migrate run check test-integration test-live-auth test-smtp-auth init-local build-backend web-dev worker probe grant local-admin

DATABASE_URL ?= postgres://heartbeat_local:heartbeat_local_only@127.0.0.1:55432/heartbeat_local?sslmode=disable
export DATABASE_URL

db-up:
	docker compose -f deploy/local/compose.yaml up -d --wait postgres

mail-up:
	docker compose -f deploy/local/compose.yaml up -d --wait mailpit

local-up:
	docker compose -f deploy/local/compose.yaml up -d --wait

init-local:
	@mkdir -p .local
	@test -s .local/probe-secret || (umask 077 && openssl rand -hex 32 > .local/probe-secret)
	@chmod 600 .local/probe-secret

build-backend:
	cd backend && go build -o bin/api ./cmd/api
	cd backend && go build -o bin/worker ./cmd/worker
	cd backend && go build -o bin/probe ./cmd/probe
	cd backend && go build -o bin/local-grant ./cmd/local-grant
	cd backend && go build -o bin/local-admin ./cmd/local-admin

db-down:
	docker compose -f deploy/local/compose.yaml down

migrate:
	cd backend && go run ./cmd/migrate

run:
	cd backend && APP_ENV="$${APP_ENV:-local}" AUTH_ORIGIN="$${AUTH_ORIGIN:-http://127.0.0.1:3000}" \
		AUTH_COOKIE_SECURE="$${AUTH_COOKIE_SECURE:-false}" \
		TURNSTILE_LOCAL_TEST_MODE="$${TURNSTILE_LOCAL_TEST_MODE:-true}" \
		TURNSTILE_SECRET_KEY="$${TURNSTILE_SECRET_KEY:-1x0000000000000000000000000000000AA}" \
		TURNSTILE_HOSTNAME="$${TURNSTILE_HOSTNAME:-localhost}" \
		SMTP_ADDR="$${SMTP_ADDR:-127.0.0.1:51025}" \
		SMTP_FROM="$${SMTP_FROM:-no-reply@heartbeat.local}" \
		go run ./cmd/api

web-dev:
	cd apps/web && npm run dev -- --hostname 127.0.0.1

worker: init-local
	cd backend && APP_ENV="$${APP_ENV:-local}" \
		PROBE_URL="$${PROBE_URL:-http://127.0.0.1:8090}" \
		PROBE_SHARED_SECRET="$$(cat ../.local/probe-secret)" go run ./cmd/worker

probe: init-local
	cd backend && PROBE_HTTP_ADDR="$${PROBE_HTTP_ADDR:-127.0.0.1:8090}" \
		PROBE_SHARED_SECRET="$$(cat ../.local/probe-secret)" go run ./cmd/probe

# Explicit local test entitlement; no default entitlement is granted on registration.
grant:
	@test -n "$(ACCOUNT_ID)" -a -n "$(LIMIT)" -a -n "$(UNTIL)" || (echo 'Set ACCOUNT_ID, LIMIT and UNTIL (RFC3339)'; exit 1)
	cd backend && APP_ENV=local go run ./cmd/local-grant --account-id "$(ACCOUNT_ID)" --limit "$(LIMIT)" --until "$(UNTIL)" --reason 'Explicit local development grant'

# Secret input is a file path only; never pass a password through Make or argv.
local-admin:
	cd backend && go build -o bin/local-admin ./cmd/local-admin
	cd backend && APP_ENV=local ./bin/local-admin \
		--email "$${ADMIN_EMAIL}" --name "$${ADMIN_NAME}" --quota "$${ADMIN_QUOTA}" \
		--until "$${ADMIN_UNTIL}" --password-file "$${ADMIN_PASSWORD_FILE}" --reason "$${ADMIN_REASON}"

check:
	cd backend && test -z "$$(gofmt -l .)"
	cd backend && go vet ./...
	cd backend && go test ./...
	cd backend && go build -o /dev/null ./cmd/api
	cd backend && go build -o /dev/null ./cmd/migrate
	cd backend && go build -o /dev/null ./cmd/worker
	cd backend && go build -o /dev/null ./cmd/probe
	cd backend && go build -o /dev/null ./cmd/local-grant
	cd backend && go build -o /dev/null ./cmd/local-admin

test-integration: db-up
	cd backend && HEARTBEAT_TEST_DATABASE_URL="$${DATABASE_URL}" go test -count=1 ./...

test-live-auth: local-up
	cd backend && HEARTBEAT_LIVE_AUTH=true HEARTBEAT_TEST_DATABASE_URL="$${DATABASE_URL}" go test -count=1 ./internal/modules/auth -run '^TestLiveAuthProviderSmoke$$'

test-smtp-auth: local-up
	cd backend && HEARTBEAT_LIVE_AUTH=true HEARTBEAT_TEST_DATABASE_URL="$${DATABASE_URL}" go test -count=1 ./internal/modules/auth -run '^TestLocalSMTPAuthSmoke$$'

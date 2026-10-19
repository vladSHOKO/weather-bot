COMPOSE ?= docker compose
GOOSE ?= goose
GO ?= go
GOOSE_VERSION := v3.27.2
GOVULNCHECK_VERSION := v1.9.0
GITLEAKS_VERSION := v8.28.0
GITLEAKS ?= gitleaks

.PHONY: start stop migrate db-roles db-passwords db-grants reset-local

start:
	$(COMPOSE) up -d

stop:
	$(COMPOSE) stop

# Goose loads GOOSE_DBSTRING from .env; credentials are not command arguments.
migrate:
	@GOOSE_DRIVER=postgres $(GOOSE) -dir ./migrations up

db-roles:
	@$(COMPOSE) exec -T db sh -c 'exec psql -X -v ON_ERROR_STOP=1 -U "$$POSTGRES_USER" -d "$$POSTGRES_DB"' < scripts/db-roles.sql

# psql encrypts passwords before sending ALTER ROLE to the server.
db-passwords:
	@$(COMPOSE) exec db sh -c 'exec psql -X -v ON_ERROR_STOP=1 -U "$$POSTGRES_USER" -d "$$POSTGRES_DB" -c "\password weather_migrator"'
	@$(COMPOSE) exec db sh -c 'exec psql -X -v ON_ERROR_STOP=1 -U "$$POSTGRES_USER" -d "$$POSTGRES_DB" -c "\password weather_app"'

db-grants:
	@$(COMPOSE) exec -T db sh -c 'exec psql -X -v ON_ERROR_STOP=1 -U "$$POSTGRES_USER" -d "$$POSTGRES_DB"' < scripts/db-grants.sql

# Deliberately separate from stop. Requires both explicit opt-ins.
reset-local:
	@test "$(APP_ENV)" = development || { echo 'Requires APP_ENV=development'; exit 1; }
	@test "$(CONFIRM_RESET)" = DELETE_LOCAL_DATA || { echo 'Requires CONFIRM_RESET=DELETE_LOCAL_DATA'; exit 1; }
	$(COMPOSE) down -v --remove-orphans

.PHONY: check check-secrets test-live-identity test-live test-integration test-deploy tools
check:
	@test -z "$$(gofmt -l main.go main_test.go internal tests/integration/fakeapi)" || { echo 'Run go fmt ./...'; exit 1; }
	bash -n scripts/backup.sh scripts/check-secrets.sh tests/integration/block-01.sh tests/integration/deploy.sh
	python3 tests/scripts/test_backup.py
	$(GO) test ./...
	$(GO) vet ./...
	$(GO) build -o /tmp/weather-sum-check .

test-integration:
	RUN_ALL_TESTS=1 GOOSE_BINARY=$(GOOSE) GO_BINARY=$(GO) bash tests/integration/block-01.sh

test-deploy:
	bash tests/integration/deploy.sh

check-secrets:
	GITLEAKS=$(GITLEAKS) bash scripts/check-secrets.sh

# Opt-in: identity is read-only; scenario sends replies to the authorized chat.
test-live-identity:
	LIVE_BOT_TESTS=1 $(GO) test -count=1 -v ./internal/bot -run '^TestLiveBotIdentity$$'

test-live:
	LIVE_BOT_TESTS=1 $(GO) test -count=1 -v ./internal/bot -run '^TestLiveBotScenario$$'

tools:
	$(GO) install -tags='no_clickhouse no_libsql no_mssql no_mysql no_sqlite3 no_vertica no_ydb' github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION)
	$(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	$(GO) install github.com/zricethezav/gitleaks/v8@$(GITLEAKS_VERSION)

#!/usr/bin/env bash
# Uses a disposable Compose project. Never loads the project's real .env.
set -euo pipefail

project_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
test_directory=$(mktemp -d /tmp/weather-sum-block1.XXXXXXXX)
chmod 700 "$test_directory"
export TEST_COMPOSE_ENV="$test_directory/test.env"
export TEST_COMPOSE_PROJECT="weather-sum-block1-${test_directory##*.}"
TEST_COMPOSE_PROJECT=${TEST_COMPOSE_PROJECT,,}
export TEST_COMPOSE_FILE="$project_root/compose.yml"

python3 - <<'PY'
import os, secrets
from pathlib import Path
p = Path(os.environ['TEST_COMPOSE_ENV'])
p.write_text('POSTGRES_USER=postgres\nPOSTGRES_DB=application\nPOSTGRES_PORT=0\nPOSTGRES_PASSWORD=' + secrets.token_hex(24) + '\n')
p.chmod(0o600)
PY

cat > "$test_directory/compose" <<'SH'
#!/bin/sh
# Host exports must not override the disposable credentials from --env-file.
unset POSTGRES_USER POSTGRES_PASSWORD POSTGRES_DB POSTGRES_PORT
exec docker compose --env-file "$TEST_COMPOSE_ENV" -p "$TEST_COMPOSE_PROJECT" -f "$TEST_COMPOSE_FILE" "$@"
SH
chmod 700 "$test_directory/compose"
compose="$test_directory/compose"
cleanup() {
    "$compose" down -v --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

run_make() { make --no-print-directory -s -C "$project_root" COMPOSE="$compose" "$@"; }
sql() { "$compose" exec -T db psql -X -v ON_ERROR_STOP=1 -U postgres -d application "$@"; }

# Fail if the guarded reset unexpectedly reaches Docker.
if run_make reset-local APP_ENV=production CONFIRM_RESET=DELETE_LOCAL_DATA >"$test_directory/reset.log" 2>&1; then
    echo 'FAIL: reset accepted a production environment'; exit 1
fi
if run_make reset-local APP_ENV=development CONFIRM_RESET= >"$test_directory/reset.log" 2>&1; then
    echo 'FAIL: reset accepted missing confirmation'; exit 1
fi

run_make start
ready=false
for attempt in {1..60}; do
    if "$compose" exec -T db pg_isready -h 127.0.0.1 -U postgres -d application >/dev/null 2>&1; then ready=true; break; fi
    sleep 1
done
if [ "$ready" != true ]; then echo 'FAIL: database not ready'; exit 1; fi

binding=$("$compose" port db 5432)
[[ "$binding" == 127.0.0.1:* ]] || { echo 'FAIL: database is not bound to loopback'; exit 1; }
run_make db-roles
# Provisioning should be repeatable without broadening privileges.
run_make db-roles
# Password-free trusted socket is used only inside the disposable container.
# The real goose binary runs on the host, with synthetic credentials in its env.
port=${binding##*:}
export TEST_DATABASE_URL="postgres://postgres:$(python3 -c 'import os; from pathlib import Path; print(dict(line.split("=",1) for line in Path(os.environ["TEST_COMPOSE_ENV"]).read_text().splitlines())["POSTGRES_PASSWORD"])')@127.0.0.1:$port/application?sslmode=disable"
goose_binary=${GOOSE_BINARY:-goose}
GOOSE_DRIVER=postgres GOOSE_DBSTRING="$TEST_DATABASE_URL" "$goose_binary" -env none -dir "$project_root/migrations" up
sql -c 'ALTER TABLE user_locations OWNER TO weather_migrator; ALTER TABLE goose_db_version OWNER TO weather_migrator; ALTER TABLE bot_state OWNER TO weather_migrator; ALTER TABLE bot_updates OWNER TO weather_migrator; ALTER VIEW bot_schema_version OWNER TO weather_migrator' >/dev/null
run_make db-grants

sql <<'SQL' >/dev/null
SET ROLE weather_app;
INSERT INTO user_locations (chat_id, location_name, latitude, longitude, timezone)
VALUES (1, 'Original', 55.75, 37.61, 'Europe/Moscow');
UPDATE user_locations SET location_name = 'Updated' WHERE chat_id = 1;
INSERT INTO user_locations (chat_id, location_name, latitude, longitude, timezone)
VALUES (2, 'Delete me', 0, 0, 'UTC');
DELETE FROM user_locations WHERE chat_id = 2;
SQL

for statement in \
    'DROP TABLE user_locations' \
    'CREATE TABLE forbidden (id int)' \
    'CREATE SCHEMA forbidden' \
    'SELECT * FROM goose_db_version' \
    'CREATE ROLE forbidden'; do
    if sql -c "SET ROLE weather_app; $statement" >"$test_directory/denied.log" 2>&1; then
        echo "FAIL: application role allowed $statement"; exit 1
    fi
    if ! grep -Eq 'permission denied|must be' "$test_directory/denied.log"; then
        cat "$test_directory/denied.log"; echo 'FAIL: unexpected SQL failure'; exit 1
    fi
done

run_make stop
run_make start
ready=false
for attempt in {1..60}; do
    if "$compose" exec -T db pg_isready -h 127.0.0.1 -U postgres -d application >/dev/null 2>&1; then ready=true; break; fi
    sleep 1
done
if [ "$ready" != true ]; then echo 'FAIL: database not ready after restart'; exit 1; fi
result=$(sql -Atc "SET ROLE weather_app; SELECT location_name FROM user_locations WHERE chat_id = 1" | tail -n 1)
[[ "$result" == Updated ]] || { echo 'FAIL: data did not survive stop/start'; exit 1; }
if [[ "${RUN_ALL_TESTS:-0}" == 1 ]]; then
    # Docker may allocate a different random published port after stop/start.
    binding=$("$compose" port db 5432)
    port=${binding##*:}
    export TEST_DATABASE_URL="postgres://postgres:$(python3 -c 'import os; from pathlib import Path; print(dict(line.split("=",1) for line in Path(os.environ["TEST_COMPOSE_ENV"]).read_text().splitlines())["POSTGRES_PASSWORD"])')@127.0.0.1:$port/application?sslmode=disable"
    GOOSE_DRIVER=postgres GOOSE_DBSTRING="$TEST_DATABASE_URL" "$goose_binary" -env none -dir "$project_root/migrations" down
    GOOSE_DRIVER=postgres GOOSE_DBSTRING="$TEST_DATABASE_URL" "$goose_binary" -env none -dir "$project_root/migrations" up
    sql -c 'ALTER TABLE bot_state OWNER TO weather_migrator; ALTER TABLE bot_updates OWNER TO weather_migrator; ALTER VIEW bot_schema_version OWNER TO weather_migrator' >/dev/null
    run_make db-grants
    (cd "$project_root" && "${GO_BINARY:-go}" test -p 1 -race -count=1 ./internal/queue ./internal/storage ./internal/app)
    # A real logical backup must restore rows and the durable checkpoint.
    (
        unset POSTGRES_USER POSTGRES_PASSWORD POSTGRES_DB POSTGRES_PORT
        export COMPOSE_ENV_FILES="$TEST_COMPOSE_ENV" COMPOSE_DISABLE_ENV_FILE=true
        export COMPOSE_PROJECT_NAME="$TEST_COMPOSE_PROJECT" COMPOSE_FILE="$TEST_COMPOSE_FILE"
        BACKUP_DIR="$test_directory/backups" bash "$project_root/scripts/backup.sh"
    )
    backup_files=("$test_directory"/backups/weather-sum-*.dump)
    [[ ${#backup_files[@]} == 1 && -s "${backup_files[0]}" ]] || { echo 'FAIL: backup script did not publish an archive'; exit 1; }
    "$compose" exec -T db createdb -U postgres restore_check
    "$compose" exec -T db pg_restore -U postgres -d restore_check < "${backup_files[0]}"
    restored=$("$compose" exec -T db psql -X -At -U postgres -d restore_check -c "SELECT location_name FROM user_locations WHERE chat_id=1")
    [[ "$restored" == Updated ]] || { echo 'FAIL: backup restoration mismatch'; exit 1; }
    echo 'PASS: durable queue, repository, workers, migrations and backup restoration.'
fi
echo 'PASS: stop preserves data; app CRUD works; DDL, role creation and migration metadata access denied; port bound to loopback; reset guarded.'

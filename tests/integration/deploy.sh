#!/usr/bin/env bash
# Exercises the real deployment configuration with disposable data and fake HTTPS.
set -euo pipefail
project_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
test_directory=$(mktemp -d /tmp/weather-sum-deploy.XXXXXXXX)
test_project="weather-sum-deploy-${test_directory##*.}"
test_project=${test_project,,}
export TEST_FIXTURE_DIR="$test_directory/fixture"
mkdir -m 755 "$TEST_FIXTURE_DIR"
export DEPLOY_RUNTIME_IMAGE=${DEPLOY_RUNTIME_IMAGE:-weather-sum:deploy-test}
export DEPLOY_MIGRATOR_IMAGE=${DEPLOY_MIGRATOR_IMAGE:-weather-sum-migrator:deploy-test}
export DEPLOY_FAKEAPI_IMAGE=${DEPLOY_FAKEAPI_IMAGE:-weather-sum-fakeapi:deploy-test}
export DEPLOY_TEST_DIRECTORY="$test_directory"
python3 - <<'PY'
import os, secrets
from pathlib import Path
root = Path(os.environ['DEPLOY_TEST_DIRECTORY'])
admin, app, migrator = (secrets.token_hex(24) for _ in range(3))
(root / 'test.env').write_text(f'''POSTGRES_USER=postgres
POSTGRES_DB=application
POSTGRES_PASSWORD={admin}
DATABASE_URL=postgres://weather_app:{app}@db/application?sslmode=disable
GOOSE_DBSTRING=postgres://weather_migrator:{migrator}@db/application?sslmode=disable
TELEGRAM_BOT_TOKEN=123:synthetic-deploy-test
OPENWEATHER_API_KEY=
WEATHERAPI_API_KEY=synthetic-weatherapi-deploy
MET_NO_USER_AGENT=weather-sum/1.0 synthetic@example.org
CHAT_COOLDOWN=1ns
REQUESTS_PER_SECOND=30
''')
(root / 'passwords.sql').write_text(f"ALTER ROLE weather_app PASSWORD '{app}';\nALTER ROLE weather_migrator PASSWORD '{migrator}';\n")
(root / 'test.env').chmod(0o600)
(root / 'passwords.sql').chmod(0o600)
PY
cat > "$test_directory/override.yml" <<'YAML'
services:
  bot:
    image: ${DEPLOY_RUNTIME_IMAGE}
    environment:
      SSL_CERT_FILE: /fixture/cert.pem
    volumes:
      - ${TEST_FIXTURE_DIR}:/fixture:ro,z
    depends_on:
      fakeapi:
        condition: service_healthy
  migrate:
    image: ${DEPLOY_MIGRATOR_IMAGE}
  fakeapi:
    image: ${DEPLOY_FAKEAPI_IMAGE}
    build:
      context: ${DEPLOY_PROJECT_ROOT}
      target: test-api
    volumes:
      - ${TEST_FIXTURE_DIR}:/fixture:ro,z
    read_only: true
    cap_drop: [ALL]
    security_opt: [no-new-privileges:true]
    healthcheck:
      test: [CMD, fakeapi, -status]
      interval: 1s
      timeout: 2s
      retries: 10
    networks:
      outbound:
        aliases: [api.telegram.org, api.open-meteo.com, geocoding-api.open-meteo.com, api.met.no, api.weatherapi.com]
networks:
  outbound:
    internal: true
YAML
compose() (
    # Never let the host's production settings override the synthetic env file.
    unset POSTGRES_USER POSTGRES_PASSWORD POSTGRES_DB DATABASE_URL GOOSE_DBSTRING
    unset TELEGRAM_BOT_TOKEN OPENWEATHER_API_KEY WEATHERAPI_API_KEY MET_NO_USER_AGENT CHAT_COOLDOWN REQUESTS_PER_SECOND
    unset HTTP_TIMEOUT DB_TIMEOUT POLL_TIMEOUT SEND_TIMEOUT COMMAND_TIMEOUT
    unset SHUTDOWN_TIMEOUT CACHE_TTL CACHE_CAPACITY QUEUE_CAPACITY WORKERS MAX_ATTEMPTS
    docker compose --env-file "$test_directory/test.env" -p "$test_project" \
        -f "$project_root/compose.deploy.yml" -f "$test_directory/override.yml" "$@"
)
cleanup() {
    test_result=$?
    if [[ "$test_result" != 0 ]]; then compose logs --tail 30 bot fakeapi || true; fi
    compose down -v --remove-orphans > /dev/null 2>&1 || true
}
trap cleanup EXIT
sql() { compose exec -T db psql -X -v ON_ERROR_STOP=1 -U postgres -d application "$@"; }
wait_database() {
    for attempt in {1..60}; do
        if compose exec -T db pg_isready -h 127.0.0.1 -U postgres -d application > /dev/null 2>&1; then return; fi
        sleep 1
    done
    echo 'FAIL: deploy database not ready'; exit 1
}
export DEPLOY_PROJECT_ROOT="$project_root"
compose config --quiet
if [[ ${DEPLOY_SKIP_BUILD:-0} != 1 ]]; then compose build bot migrate fakeapi; fi
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj '/CN=weather-sum deploy test' \
    -addext 'subjectAltName=DNS:api.telegram.org,DNS:api.open-meteo.com,DNS:geocoding-api.open-meteo.com,DNS:api.met.no,DNS:api.weatherapi.com,IP:127.0.0.1' \
    -keyout "$TEST_FIXTURE_DIR/key.pem" -out "$TEST_FIXTURE_DIR/cert.pem" > /dev/null 2>&1
# These are disposable test credentials, readable by the unprivileged container.
chmod 644 "$TEST_FIXTURE_DIR/key.pem" "$TEST_FIXTURE_DIR/cert.pem"
compose up -d db
wait_database
sql < "$project_root/scripts/db-roles.sql" > /dev/null
sql < "$test_directory/passwords.sql" > /dev/null
if compose run --rm --no-deps bot > "$test_directory/startup.log" 2>&1; then
    echo 'FAIL: bot accepted an unmigrated database'; exit 1
fi
grep -q 'database schema check' "$test_directory/startup.log" || { echo 'FAIL: unexpected schema startup failure'; exit 1; }
compose --profile tools run --rm migrate
sql < "$project_root/scripts/db-grants.sql" > /dev/null
for setting in TELEGRAM_BOT_TOKEN= HTTP_TIMEOUT=0s; do
    if compose run --rm --no-deps -e "$setting" bot > "$test_directory/invalid.log" 2>&1; then
        echo 'FAIL: bot accepted invalid configuration'; exit 1
    fi
    grep -q "${setting%%=*}" "$test_directory/invalid.log" || { echo 'FAIL: unexpected configuration startup failure'; exit 1; }
done
compose up -d bot
completed=false
for attempt in {1..60}; do
    count=$(sql -Atc "SELECT count(*) FROM bot_updates WHERE status='done'" | tail -n 1)
    if [[ "$count" == 10 ]]; then completed=true; break; fi
    sleep 1
done
[[ "$completed" == true ]] || { echo 'FAIL: deployment did not complete synthetic flow'; compose logs --tail 30 bot; exit 1; }
compose exec -T bot weather-sum -healthcheck
status=$(compose exec -T fakeapi fakeapi -status)
python3 - "$status" <<'PY'
import json, sys
s = json.loads(sys.argv[1])
assert s == dict(replies=11, forecasts=2, met_forecasts=1, weatherapi_forecasts=2, sources=True, tomorrow=True, searches=1, weather=True, deleted=True, missing=True), s
PY
bot_container=$(compose ps -q bot)
security=$(docker inspect --format '{{.Config.User}} {{.HostConfig.ReadonlyRootfs}} {{json .HostConfig.CapDrop}}' "$bot_container")
[[ "$security" == '65532:65532 true ["ALL"]' ]] || { echo 'FAIL: runtime privilege configuration'; exit 1; }
db_container=$(compose ps -q db)
bindings=$(docker inspect --format '{{len .HostConfig.PortBindings}}' "$db_container")
[[ "$bindings" == 0 ]] || { echo 'FAIL: deploy database has a published port'; exit 1; }
remaining=$(sql -Atc 'SELECT count(*) FROM user_locations WHERE chat_id=7' | tail -n 1)
[[ "$remaining" == 0 ]] || { echo 'FAIL: synthetic location was not deleted'; exit 1; }
compose stop bot
compose up -d bot
for attempt in {1..30}; do
    if compose exec -T bot weather-sum -healthcheck; then break; fi
    sleep 1
done
compose exec -T bot weather-sum -healthcheck
sleep 1
after=$(compose exec -T fakeapi fakeapi -status)
[[ "$after" == "$status" ]] || { echo 'FAIL: completed commands replayed after restart'; exit 1; }
compose stop db
if compose exec -T bot weather-sum -healthcheck > "$test_directory/health-unavailable.log" 2>&1; then echo 'FAIL: readiness ignored unavailable database'; exit 1; fi
if compose run --rm --no-deps bot > "$test_directory/unavailable.log" 2>&1; then
    echo 'FAIL: bot accepted an unavailable database'; exit 1
fi
grep -q 'database schema check' "$test_directory/unavailable.log" || { echo 'FAIL: unexpected database startup failure'; exit 1; }
echo 'PASS: deploy startup validation, migration/app role login, HTTPS command flow, healthcheck, unprivileged runtime, DB isolation and restart checkpoint.'

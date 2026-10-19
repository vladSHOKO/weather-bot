#!/usr/bin/env bash
# Run as root on an already provisioned server. Secrets stay under /etc.
set -euo pipefail
release=$(realpath "${1:?Pass the release directory}")
[[ "$release" == /opt/weather-sum/releases/* ]] || exit 1
test -x "$release/weather-sum"
test -r /etc/weather-sum/app.env
test -r /etc/weather-sum/migrate.env
test -x /usr/local/lib/weather-sum/goose

systemctl stop weather-sum.service 2>/dev/null || true
(
    set -a
    source /etc/weather-sum/migrate.env
    set +a
    /usr/local/lib/weather-sum/goose -env none -dir "$release/migrations" up
)
runuser -u postgres -- psql -X -v ON_ERROR_STOP=1 -d weather_bot -f "$release/scripts/db-grants.sql"
install -m 644 "$release"/deploy/systemd/* /etc/systemd/system/
ln -sfn "$release" /opt/weather-sum/current
systemctl daemon-reload
systemctl enable --now weather-sum.service
for attempt in {1..30}; do
    if "$release/weather-sum" -healthcheck; then
        printf 'Ready: %s\n' "$release"
        exit 0
    fi
    sleep 1
done
systemctl status weather-sum.service --no-pager
exit 1

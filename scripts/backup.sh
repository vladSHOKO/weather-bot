#!/usr/bin/env bash
set -euo pipefail
umask 077
backup_directory=${BACKUP_DIR:-./backups}
compose_file=${COMPOSE_FILE:-compose.deploy.yml}
mkdir -p -- "$backup_directory"
chmod 700 -- "$backup_directory"
backup_file="$backup_directory/weather-sum-$(date -u +%Y%m%dT%H%M%SZ).dump"
# Publish only a successful, readable archive. Temporary files stay on the same
# filesystem so linking to the final name is atomic and cannot replace a copy.
partial_file=$(mktemp "$backup_directory/.weather-sum-XXXXXXXX.partial")
trap 'rm -f -- "$partial_file"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
docker compose -f "$compose_file" exec -T db sh -c 'exec pg_dump -Fc -U "$POSTGRES_USER" -d "$POSTGRES_DB"' > "$partial_file"
test -s "$partial_file"
docker compose -f "$compose_file" exec -T db pg_restore --list < "$partial_file" > /dev/null
ln -- "$partial_file" "$backup_file"
find "$backup_directory" -maxdepth 1 -type f -name 'weather-sum-*.dump' -mmin +20160 -delete
printf 'Backup completed: %s\n' "$backup_file"

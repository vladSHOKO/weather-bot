#!/usr/bin/env bash
# Build locally and upload a committed release. No secrets enter the archive.
set -euo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"
target=${1:?Usage: bash scripts/deploy-systemd.sh root@server}
[[ "$target" =~ ^[a-zA-Z0-9_.@:-]+$ ]] || exit 1
test -z "$(git status --porcelain)" || { echo 'Commit changes before deploying.' >&2; exit 1; }
revision=$(git rev-parse --short=12 HEAD)
release="/opt/weather-sum/releases/$(date -u +%Y%m%dT%H%M%SZ)-$revision"
build_directory=$(mktemp -d /tmp/weather-sum-release.XXXXXXXX)
chmod 755 "$build_directory"
trap 'rm -rf -- "$build_directory"' EXIT
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$build_directory/weather-sum" .
git archive HEAD deploy migrations scripts/db-grants.sql | tar -x -C "$build_directory"
printf '%s\n' "$(git rev-parse HEAD)" > "$build_directory/REVISION"
tar -C "$build_directory" -czf - . | ssh "$target" "umask 022; mkdir -p '$release' && tar -xzf - -C '$release'"
ssh "$target" "bash '$release/deploy/install-release.sh' '$release'"

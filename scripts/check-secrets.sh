#!/usr/bin/env bash
# Scan publishable sources, plus tracked secrets and history when Git exists.
# Local runtime secrets are never copied into a source-only snapshot.
set -euo pipefail
umask 077
project_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
scan_directory=$(mktemp -d /tmp/weather-sum-secrets.XXXXXXXX)
export WEATHER_SCAN_ROOT="$project_root" WEATHER_SCAN_DIRECTORY="$scan_directory"
python3 - <<'PY'
import os, shutil, subprocess
from pathlib import Path
root, scan = Path(os.environ['WEATHER_SCAN_ROOT']), Path(os.environ['WEATHER_SCAN_DIRECTORY'])
excluded_dirs = {'.git', '.idea', '.agents', '.codex', '.aws', 'postgres_data', 'backups', 'bin', 'node_modules', 'vendor', '__pycache__'}
selected = set()
for directory, dirs, files in os.walk(root, followlinks=False):
    dirs[:] = [d for d in dirs if d not in excluded_dirs and not (Path(directory)/d).is_symlink()]
    for name in files:
        path = Path(directory)/name
        if name == '.env' or name.startswith('.env.') and name != '.env.example':
            continue
        if path.is_symlink() or path.suffix in {'.dump', '.backup', '.log'} or path == root/'weather-sum':
            continue
        selected.add(path.relative_to(root))
# Tracked files must be scanned even if a local ignore/exclusion would hide them.
tracked = subprocess.run(['git', '-C', str(root), 'ls-files', '-z'], capture_output=True)
if tracked.returncode == 0:
    for raw in tracked.stdout.split(b'\0'):
        if raw:
            relative = Path(os.fsdecode(raw))
            if (root/relative).is_file() and not (root/relative).is_symlink():
                selected.add(relative)
if not selected:
    raise SystemExit('FAIL: no sources selected for secret scanning')
source = scan/'source'
source.mkdir(mode=0o700)
for relative in sorted(selected):
    target = source/relative
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(root/relative, target)
print(f'Selected {len(selected)} source files; local runtime secrets excluded unless tracked.')
PY
gitleaks_binary=${GITLEAKS:-gitleaks}
"$gitleaks_binary" dir "$scan_directory/source" --redact --no-banner \
    --report-format json --report-path "$scan_directory/source-report.json"
if git -C "$project_root" rev-parse --verify HEAD > /dev/null 2>&1; then
    (cd "$project_root" && "$gitleaks_binary" git --redact --no-banner --log-opts='--all' \
        --report-format json --report-path "$scan_directory/history-report.json")
else
    echo 'History scan: not applicable to this copy (no Git commits). Scan full history when introducing/importing a repository.'
fi
printf 'Secret scan reports: %s\n' "$scan_directory"

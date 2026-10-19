"""Backup failure/rotation checks; no Docker daemon or real credentials required."""
import os
from pathlib import Path
import stat
import subprocess
import tempfile
import time
import unittest

ROOT = Path(__file__).resolve().parents[2]


class BackupTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="weather-sum-backup-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.backups = self.root / "backups"
        self.backups.mkdir()
        docker = self.bin / "docker"
        docker.write_text("""#!/usr/bin/env python3
import os, sys
if 'pg_restore' in sys.argv:
    sys.exit(0 if sys.stdin.buffer.read() == b'archive' else 3)
mode = os.environ.get('BACKUP_TEST_MODE', 'success')
sys.stdout.write({'success': 'archive', 'partial': 'incomplete', 'invalid': 'garbage', 'empty': ''}[mode])
sys.exit(2 if mode == 'partial' else 0)
""")
        docker.chmod(0o700)
        date = self.bin / "date"
        date.write_text("#!/bin/sh\nprintf '20261019T010203Z\\n'\n")
        date.chmod(0o700)
        self.old = self.backups / "weather-sum-old.dump"
        self.old.write_bytes(b"old archive")
        old = time.time() - 15 * 86400
        os.utime(self.old, (old, old))
        self.other = self.backups / "other.dump"
        self.other.write_bytes(b"unrelated")
        os.utime(self.other, (old, old))
        self.final = self.backups / "weather-sum-20261019T010203Z.dump"

    def run_backup(self, mode="success"):
        env = dict(os.environ, PATH=str(self.bin) + os.pathsep + os.environ["PATH"],
                   BACKUP_DIR=str(self.backups), BACKUP_TEST_MODE=mode)
        return subprocess.run(["bash", str(ROOT / "scripts/backup.sh")], env=env,
                              capture_output=True, text=True)

    def assert_no_partial(self):
        self.assertEqual(list(self.backups.glob("*.partial")), [])

    def test_success(self):
        result = self.run_backup()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.final.read_bytes(), b"archive")
        self.assertEqual(stat.S_IMODE(self.final.stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(self.backups.stat().st_mode), 0o700)
        self.assertFalse(self.old.exists())
        self.assertTrue(self.other.exists())
        self.assert_no_partial()

    def test_failures_never_publish_or_prune(self):
        for mode in ["partial", "empty", "invalid"]:
            with self.subTest(mode=mode):
                self.assertNotEqual(self.run_backup(mode).returncode, 0)
                self.assertFalse(self.final.exists())
                self.assertTrue(self.old.exists())
                self.assert_no_partial()

    def test_collision_keeps_existing_copy(self):
        self.final.write_bytes(b"previous archive")
        self.assertNotEqual(self.run_backup().returncode, 0)
        self.assertEqual(self.final.read_bytes(), b"previous archive")
        self.assertTrue(self.old.exists())
        self.assert_no_partial()


if __name__ == "__main__":
    unittest.main()

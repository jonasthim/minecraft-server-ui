"""Local helper fixtures only: never starts/stops an installed service."""
import contextlib
import io
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import tempfile
import unittest
from unittest import mock

import admin_recovery as recovery


class RecoveryHelperTests(unittest.TestCase):
    def fixture(self, root):
        data = root / "source"
        data.mkdir()
        database = sqlite3.connect(data / "manager.db")
        database.execute("PRAGMA journal_mode=WAL")
        database.execute("PRAGMA wal_autocheckpoint=0")
        database.execute("CREATE TABLE users(username TEXT,role TEXT,disabled INTEGER)")
        database.execute("INSERT INTO users VALUES('native-smoke-admin','admin',0)")
        database.commit()
        self.addCleanup(database.close)
        return data

    def test_private_snapshot_reads_required_uncheckpointed_wal(self):
        temporary = self.enterContext(tempfile.TemporaryDirectory())
        root = Path(temporary)
        data = self.fixture(root)
        snapshot = root / "snapshot"
        snapshot.mkdir()
        for name in recovery.SIDECARS:
            if (data / name).exists():
                shutil.copyfile(data / name, snapshot / name)
        self.assertTrue((snapshot / "manager.db-wal").stat().st_size > 0)
        recovery._verify_snapshot(snapshot)
        # Removing WAL must break this assertion: base DB lacks the schema.
        (snapshot / "manager.db-wal").unlink()
        with self.assertRaises(sqlite3.DatabaseError):
            recovery._verify_snapshot(snapshot)

    def test_snapshot_orchestration_cleans_private_copy(self):
        temporary = self.enterContext(tempfile.TemporaryDirectory())
        data = self.fixture(Path(temporary))
        copied = []

        def fixture_snapshot(command, **options):
            destination = Path(command[-1])
            copied.append(destination)
            for name in recovery.SIDECARS:
                if (data / name).exists():
                    shutil.copyfile(data / name, destination / name)
            count = len(list(destination.iterdir()))
            report = {"snapshot_files": count, "complete_sidecars": True,
                      "hash_match": True, "lock_contention_refused": True}
            return subprocess.CompletedProcess(command, 0, json.dumps(report).encode(), b"")

        with mock.patch.object(recovery.subprocess, "run", side_effect=fixture_snapshot):
            count = recovery._snapshot(data)
        self.assertGreaterEqual(count, 2)
        self.assertEqual(len(copied), 1)
        self.assertFalse(copied[0].exists())

    def test_snapshot_failure_cleans_private_directory(self):
        with tempfile.TemporaryDirectory() as temporary:
            data = Path(temporary)
            result = subprocess.CompletedProcess([], 1, b"private-subprocess-output", b"private-error")
            with mock.patch.object(recovery.subprocess, "run", return_value=result):
                with self.assertRaisesRegex(RuntimeError, "^recovery assertion failed$"):
                    recovery._snapshot(data)
            self.assertEqual(list(data.iterdir()), [])

    def test_cli_password_only_in_closed_stdin_and_captured_output(self):
        password = "private-in-memory-fixture-password"
        result = subprocess.CompletedProcess([], 0, b"fixed confirmation", b"")
        with mock.patch.object(recovery.subprocess, "run", return_value=result) as process:
            recovery._command(password)
        arguments, options = process.call_args
        self.assertNotIn(password, repr(arguments))
        self.assertNotIn(password, repr(options["env"]))
        self.assertEqual(options["input"], (password + "\n").encode())
        self.assertTrue(options["capture_output"])
        self.assertEqual(arguments[0][-4:], ["admin-reset", "--username", "native-smoke-admin", "--password-stdin"])

    def test_cli_secret_echo_does_not_escape_error(self):
        password = "private-echo-fixture-password"
        result = subprocess.CompletedProcess([], 0, password.encode(), b"")
        with mock.patch.object(recovery.subprocess, "run", return_value=result):
            with self.assertRaisesRegex(RuntimeError, "^recovery assertion failed$"):
                recovery._command(password)

    def test_non_hosted_guard_refuses_before_callbacks(self):
        never = mock.Mock(side_effect=AssertionError("callback must not run"))
        with mock.patch.dict(os.environ, {"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "self-hosted"}):
            with contextlib.redirect_stdout(io.StringIO()) as output:
                with self.assertRaisesRegex(RuntimeError, "^admin recovery failed at runner-guard$"):
                    recovery.qualify_recovery(client=never, request=never, ready=never,
                                              control=never, manager_pids=never, session=None,
                                              identity=1, credentials={})
        never.assert_not_called()
        self.assertEqual(output.getvalue(), "")


if __name__ == "__main__":
    unittest.main()

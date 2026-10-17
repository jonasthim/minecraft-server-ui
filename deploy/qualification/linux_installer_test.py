import importlib.util
import json
import os
from pathlib import Path
import sqlite3
import tempfile
import unittest
from types import SimpleNamespace
from unittest import mock


SCRIPT = Path(__file__).with_name("linux_installer.py")
SPEC = importlib.util.spec_from_file_location("linux_installer", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class InstallerHarnessFixtureTests(unittest.TestCase):
    def test_command_failure_keeps_only_validated_result_fields(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            output = root / "out"
            output.mkdir()
            qualification = MODULE.Qualification(root / "bootstrap", root / "candidates", output, "lifecycle")
            operation_id = "a" * 64
            stdout = json.dumps({"outcome": "manual_repair", "operation_id": operation_id,
                                "failed_stage": "replace", "image": {
                                    "version": "v0.0.2", "schema": 18, "sha256": "b" * 64}}).encode()
            with mock.patch.object(MODULE.subprocess, "run", return_value=SimpleNamespace(
                    stdout=stdout, stderr=b"secret stderr", returncode=1)):
                with self.assertRaisesRegex(RuntimeError, "unexpected status"):
                    qualification.command(["installer"], expected=0)
            self.assertEqual(qualification.last_command_result, {
                "return_code": 1,
                "result": {"outcome": "manual_repair", "operation_id": operation_id,
                           "failed_stage": "replace", "image": {
                               "version": "v0.0.2", "schema": 18, "sha256": "b" * 64}},
            })

    def test_command_diagnostic_rejects_unknown_secret_fields(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            output = root / "out"
            output.mkdir()
            qualification = MODULE.Qualification(root / "bootstrap", root / "candidates", output, "lifecycle")
            stdout = b'{"outcome":"manual_repair","failed_stage":"replace","secret":"CANARY-DO-NOT-LEAK"}'
            with mock.patch.object(MODULE.subprocess, "run", return_value=SimpleNamespace(
                    stdout=stdout, stderr=b"CANARY-DO-NOT-LEAK", returncode=1)):
                with self.assertRaisesRegex(RuntimeError, "unexpected status"):
                    qualification.command(["installer"], expected=0)
            serialized = json.dumps(qualification.last_command_result)
            self.assertEqual(qualification.last_command_result,
                             {"return_code": 1, "result": "unavailable"})
            self.assertNotIn("CANARY-DO-NOT-LEAK", serialized)
            qualification.write_summary("fail")
            summary = (output / "acceptance-summary.json").read_text(encoding="utf-8")
            self.assertNotIn("CANARY-DO-NOT-LEAK", summary)
            self.assertEqual(json.loads(summary)["last_command_result"], qualification.last_command_result)

    def test_check_parser_accepts_expected_states_and_valid_optional_image(self):
        self.assertTrue(MODULE.valid_check_report(
            {"state": "absent", "coherent": False, "pending": False}, "absent", False, False))
        self.assertTrue(MODULE.valid_check_report(
            {"state": "retained", "coherent": False, "pending": False,
             "image": {"version": "v0.0.3", "schema": 7, "sha256": "a" * 64}},
            "retained", False, False))

    def test_check_parser_rejects_unexpected_fields_and_incoherent_images(self):
        valid = {"state": "installed", "coherent": True, "pending": False,
                 "image": {"version": "v0.0.2", "schema": 7, "sha256": "b" * 64}}
        self.assertTrue(MODULE.valid_check_report(valid, "installed", True, False))
        self.assertFalse(MODULE.valid_check_report({**valid, "nonce": "secret"}, "installed", True, False))
        invalid = dict(valid)
        invalid["image"] = {"version": "v0.0.2", "schema": 0, "sha256": "b" * 64}
        self.assertFalse(MODULE.valid_check_report(invalid, "installed", True, False))

    def test_strict_json_rejects_duplicate_members(self):
        with self.assertRaises(ValueError):
            MODULE.strict_json('{"state":"absent","state":"installed"}')

    def test_workflow_download_root_matches_harness_candidate_layout(self):
        workflow = Path(__file__).parents[2] / ".github/workflows/linux-installer.yml"
        content = workflow.read_text(encoding="utf-8")
        self.assertIn('path: installer-fixtures', content)
        self.assertIn('"$GITHUB_WORKSPACE/installer-fixtures"', content)
        self.assertNotIn('"$GITHUB_WORKSPACE/installer-fixtures/installer-candidates"', content)

    def test_updater_scenario_requires_all_three_explicit_fixture_paths(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            qualifier = root / "qualifier"
            qualifier.write_bytes(b"fixture")
            qualifier.chmod(0o755)
            linux = root / "linux"
            windows = root / "windows"
            linux.mkdir()
            windows.mkdir()
            for version in ("v0.0.1", "v0.0.2", "v0.0.3"):
                linux_version = linux / version
                windows_version = windows / version
                linux_version.mkdir()
                windows_version.mkdir()
                for binary in (linux_version / "manager", windows_version / "manager.exe"):
                    binary.write_bytes(b"fixture")
                    binary.chmod(0o755)
            self.assertTrue(MODULE.validate_scenario_args("updater-coexistence", qualifier, linux, windows))
            self.assertFalse(MODULE.validate_scenario_args("updater-coexistence", qualifier, linux, None))
            self.assertFalse(MODULE.validate_scenario_args("lifecycle", qualifier, linux, windows))
            self.assertTrue(MODULE.validate_scenario_args("retained-retry"))
            self.assertFalse(MODULE.validate_scenario_args("unknown"))

    def test_workflow_wires_updater_native_peer_inputs_only_to_coexistence(self):
        workflow = Path(__file__).parents[2] / ".github/workflows/linux-installer.yml"
        content = workflow.read_text(encoding="utf-8")
        self.assertIn("scenario: [lifecycle, retained-retry, updater-coexistence]", content)
        self.assertIn("go build -trimpath -o installer-input/linux/qualify-selfupdate ./cmd/qualify-selfupdate", content)
        self.assertIn("if: matrix.scenario == 'updater-coexistence'", content)
        self.assertIn('"$GITHUB_WORKSPACE/installer-input/windows"', content)
        self.assertIn("chmod 0755 installer-input/linux/*/manager", content)
        self.assertIn("installer-input/windows/*/manager.exe", content)

    def test_database_snapshot_records_schema_and_manager_logical_state(self):
        with tempfile.TemporaryDirectory() as directory:
            database = Path(directory) / "manager.db"
            connection = sqlite3.connect(database)
            connection.executescript(
                "CREATE TABLE goose_db_version(id INTEGER PRIMARY KEY, version_id INTEGER, is_applied INTEGER);"
                "CREATE TABLE instances(id TEXT PRIMARY KEY);"
                "CREATE TABLE users(id INTEGER PRIMARY KEY, username TEXT);"
                "INSERT INTO goose_db_version VALUES(1, 24, 1);"
                "INSERT INTO instances VALUES('qualification-marker');"
                "INSERT INTO users VALUES(1, 'qualification-admin');"
            )
            connection.close()
            first = MODULE.database_snapshot(database)
            self.assertEqual(first["migration_rows"], [[24, 1]])
            self.assertEqual(first["instance_count"], 1)
            self.assertEqual(first["user_count"], 1)
            self.assertEqual(first, MODULE.database_snapshot(database))

    def test_executor_match_uses_regular_file_mode_owner_and_same_file_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            expected = Path(directory) / "executor"
            expected.write_bytes(b"fixture executable")
            expected.chmod(0o755)
            process_exe = Path(directory) / "proc-exe"
            process_exe.symlink_to(expected)
            uid = os.stat(expected).st_uid
            self.assertTrue(MODULE.executor_matches_path(expected, process_exe, uid))
            self.assertFalse(MODULE.executor_matches_path(expected, process_exe, uid + 1))
            expected.chmod(0o700)
            self.assertFalse(MODULE.executor_matches_path(expected, process_exe, uid))
            expected.unlink()
            expected.symlink_to(process_exe)
            self.assertFalse(MODULE.executor_matches_path(expected, process_exe, uid))

    def test_transaction_reader_retries_atomic_phase_replacement(self):
        operation_id = "a" * 64
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "installation-transaction.json"
            path.write_text(json.dumps({"id": operation_id, "phase": "replacing"}), encoding="utf-8")
            path.chmod(0o600)
            calls = 0

            def replace_during_observation(target):
                nonlocal calls
                before = target.lstat()
                raw = target.read_bytes()
                if calls == 0:
                    next_path = target.with_suffix(".next")
                    next_path.write_text(json.dumps({"id": operation_id, "phase": "verifying"}), encoding="utf-8")
                    next_path.chmod(0o600)
                    next_path.replace(target)
                calls += 1
                return before, raw, target.lstat()

            observed = MODULE.read_transaction_at(path, expected_owner=os.stat(path).st_uid,
                                                  observe=replace_during_observation, wait=lambda _: None)
            self.assertEqual(observed, {"id": operation_id, "phase": "verifying"})
            self.assertEqual(calls, 2)

    def test_transaction_reader_rejects_stable_malformed_protected_state(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "installation-transaction.json"
            path.write_text('{"id":17,"phase":"replacing"}', encoding="utf-8")
            path.chmod(0o600)
            with self.assertRaisesRegex(RuntimeError, "phase observation is invalid"):
                MODULE.read_transaction_at(path, expected_owner=os.stat(path).st_uid)


if __name__ == "__main__":
    unittest.main()

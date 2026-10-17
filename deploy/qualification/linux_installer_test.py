import importlib.util
import errno
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
    def test_updater_evidence_publication_copies_across_exdev_and_retains_sources(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            output = root / "out"
            source = root / "scratch" / "updater-evidence"
            output.mkdir()
            source.mkdir(parents=True)
            qualification = MODULE.Qualification(root / "bootstrap", root / "candidates", output,
                                                 "updater-coexistence")
            names = {
                "acceptance-summary.json": "updater-acceptance-summary.json",
                "activation-state-summary.json": "updater-activation-state-summary.json",
                "initial-update-status.json": "updater-initial-update-status.json",
                "interruption-boundary.json": "updater-interruption-boundary.json",
                "api-assertions.txt": "updater-api-assertions.txt",
            }
            contents = {}
            for name in names:
                contents[name] = ("bounded report: " + name + "\n").encode()
                (source / name).write_bytes(contents[name])

            with mock.patch.object(MODULE.os, "replace",
                                   side_effect=OSError(errno.EXDEV, "cross-device link")) as replace:
                qualification.publish_updater_evidence(source)

            replace.assert_not_called()
            for original, published in names.items():
                self.assertEqual((output / published).read_bytes(), contents[original])
                self.assertEqual((output / published).stat().st_mode & 0o777, 0o644)
                self.assertTrue((source / original).is_file())

    def test_updater_evidence_publication_rejects_symlink_but_copies_remaining_reports(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            output = root / "out"
            source = root / "scratch" / "updater-evidence"
            output.mkdir()
            source.mkdir(parents=True)
            qualification = MODULE.Qualification(root / "bootstrap", root / "candidates", output,
                                                 "updater-coexistence")
            outside = root / "outside-report"
            outside.write_text("PRIVATE-CANARY")
            (source / "acceptance-summary.json").symlink_to(outside)
            (source / "api-assertions.txt").write_text("bounded safe report\n")

            with self.assertRaisesRegex(RuntimeError, "could not be published safely"):
                qualification.publish_updater_evidence(source)

            self.assertEqual((output / "updater-api-assertions.txt").read_text(), "bounded safe report\n")
            self.assertFalse((output / "updater-acceptance-summary.json").exists())
            self.assertNotIn("PRIVATE-CANARY", str(output))

    def test_updater_evidence_rejects_fifo_without_blocking(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            output = root / "out"
            output.mkdir()
            source = root / "report.fifo"
            os.mkfifo(source)

            with self.assertRaisesRegex(RuntimeError, "unsafe or oversized"):
                MODULE.Qualification._copy_bounded_evidence(source, output / "report.txt")

            self.assertFalse((output / "report.txt").exists())

    def test_updater_evidence_does_not_remove_replaced_target(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            output = root / "out"
            output.mkdir()
            source = root / "report.txt"
            source.write_text("safe report")
            replaced = []
            original_info = MODULE._updater_evidence_target_entry

            def replace_before_check(output_fd, name):
                path = output / name
                path.unlink()
                path.write_text("replacement owned by another writer")
                replaced.append(path)
                return original_info(output_fd, name)

            with mock.patch.object(MODULE, "_updater_evidence_target_entry", side_effect=replace_before_check):
                with self.assertRaisesRegex(RuntimeError, "target identity changed"):
                    MODULE.Qualification._copy_bounded_evidence(source, output / "published.txt")

            self.assertEqual(len(replaced), 1)
            self.assertEqual(replaced[0].read_text(), "replacement owned by another writer")

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

    def test_install_check_failure_keeps_only_normalized_report(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            output = root / "out"
            output.mkdir()
            qualification = MODULE.Qualification(root / "bootstrap", root / "candidates", output, "lifecycle")
            stdout = json.dumps({"state": "drifted", "coherent": False, "pending": False,
                                "image": {"version": "v0.0.2", "schema": 18, "sha256": "b" * 64}}).encode()
            with mock.patch.object(MODULE.subprocess, "run", return_value=SimpleNamespace(
                    stdout=stdout, stderr=b"private diagnostic", returncode=1)):
                with self.assertRaisesRegex(RuntimeError, "unexpected status"):
                    qualification.command([root / "bootstrap", "install", "check"], expected=0)
            self.assertEqual(qualification.last_command_result["check_report"], {
                "state": "drifted", "coherent": False, "pending": False,
                "image": {"version": "v0.0.2", "schema": 18, "sha256": "b" * 64},
            })

            secret_report = b'{"state":"drifted","coherent":false,"pending":false,"debug":"PRIVATE-CANARY"}'
            with mock.patch.object(MODULE.subprocess, "run", return_value=SimpleNamespace(
                    stdout=secret_report, stderr=b"PRIVATE-CANARY", returncode=1)):
                with self.assertRaisesRegex(RuntimeError, "unexpected status"):
                    qualification.command([root / "bootstrap", "install", "check"], expected=0)
            encoded = json.dumps(qualification.last_command_result)
            self.assertEqual(qualification.last_command_result["check_report"], "unavailable")
            self.assertNotIn("PRIVATE-CANARY", encoded)

            inconsistent = b'{"state":"installed","coherent":true,"pending":true}'
            with mock.patch.object(MODULE.subprocess, "run", return_value=SimpleNamespace(
                    stdout=inconsistent, stderr=b"", returncode=1)):
                with self.assertRaisesRegex(RuntimeError, "unexpected status"):
                    qualification.command([root / "bootstrap", "install", "check"], expected=0)
            self.assertEqual(qualification.last_command_result["check_report"], "unavailable")

    def test_prepare_attempts_use_distinct_output_paths_without_case_records(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            output = root / "out"
            output.mkdir()
            qualification = MODULE.Qualification(root / "bootstrap", root / "candidates", output, "lifecycle")
            qualification.scratch = root / "scratch"
            qualification.scratch.mkdir()
            requested = []

            def fake_cli(*args, **kwargs):
                destination = Path(args[args.index("--output") + 1])
                if destination.exists():
                    raise RuntimeError("preparation output path already used")
                destination.mkdir()
                requested.append(destination)
                return SimpleNamespace(stdout=b'{"outcome":"installed"}')

            with mock.patch.object(qualification, "cli", side_effect=fake_cli):
                qualification.prepare_install("v0.0.1")
                qualification.prepare_install("v0.0.1")

            self.assertEqual(qualification.checks, [])
            self.assertEqual(len(requested), 2)
            self.assertNotEqual(requested[0], requested[1])
            self.assertTrue(all(path.is_dir() for path in requested))

    def test_retained_receipt_comparison_uses_validated_content_not_inode(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            receipt = root / "installed.json"
            body = json.dumps({"format": 1, "version": "v0.0.2", "schema": 24,
                               "sha256": "a" * 64}, separators=(",", ":")).encode()
            receipt.write_bytes(body)
            before = MODULE.installed_receipt_content(receipt)
            replacement = root / "replacement.json"
            replacement.write_bytes(body)
            replacement.replace(receipt)

            self.assertEqual(MODULE.installed_receipt_content(receipt), before)

    def test_retained_receipt_validation_rejects_unlisted_secret_fields(self):
        with tempfile.TemporaryDirectory() as directory:
            receipt = Path(directory) / "installed.json"
            receipt.write_text(json.dumps({"format": 1, "version": "v0.0.2", "schema": 24,
                                           "sha256": "a" * 64, "token": "PRIVATE-CANARY"}))

            with self.assertRaisesRegex(RuntimeError, "unavailable or invalid") as error:
                MODULE.installed_receipt_content(receipt)

            self.assertNotIn("PRIVATE-CANARY", str(error.exception))

    def test_failure_source_line_is_only_from_exact_harness_frame(self):
        try:
            source = "def caller():\n    fail()\ndef fail():\n    raise RuntimeError('CANARY-DO-NOT-LEAK')\ncaller()\n"
            exec(compile(source, MODULE.__file__, "exec"), {})
        except RuntimeError as error:
            line = MODULE.failure_source_line(error)
            lines = MODULE.failure_source_lines(error)
        else:
            self.fail("expected synthetic harness failure")
        self.assertEqual(line, 4)
        self.assertEqual(lines, [5, 2, 4])
        self.assertTrue(all(type(value) is int for value in lines))

        foreign_name = "/private/foreign/harness.py"
        try:
            exec(compile("raise RuntimeError('CANARY-DO-NOT-LEAK')", foreign_name, "exec"), {})
        except RuntimeError as error:
            foreign_line = MODULE.failure_source_line(error)
            foreign_lines = MODULE.failure_source_lines(error)
        else:
            self.fail("expected synthetic foreign failure")
        self.assertIsNone(foreign_line)
        self.assertEqual(foreign_lines, [])
        serialized = json.dumps({"failure_source_line": foreign_line, "failure_source_lines": foreign_lines})
        self.assertNotIn("CANARY-DO-NOT-LEAK", serialized)
        self.assertNotIn(foreign_name, serialized)

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

    def test_world_fixture_creates_only_missing_instances_root_as_private_owner(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory) / "instances"
            uid, gid = os.getuid(), os.getgid()
            self.assertFalse(base.exists())
            MODULE.ensure_fixture_instances_root(base, uid, gid)
            info = base.lstat()
            self.assertTrue(MODULE.stat.S_ISDIR(info.st_mode))
            self.assertEqual((info.st_uid, info.st_gid, MODULE.stat.S_IMODE(info.st_mode)), (uid, gid, 0o700))
            identity = (info.st_dev, info.st_ino)
            MODULE.ensure_fixture_instances_root(base, uid, gid)
            self.assertEqual((base.lstat().st_dev, base.lstat().st_ino), identity)

            link = Path(directory) / "instances-link"
            link.symlink_to(base, target_is_directory=True)
            with self.assertRaisesRegex(RuntimeError, "unavailable or unsafe"):
                MODULE.ensure_fixture_instances_root(link, uid, gid)

    def test_qualification_helper_is_copied_out_of_private_source_ancestry(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            private_source_parent = root / "runner-private"
            private_source_parent.mkdir(mode=0o700)
            source = private_source_parent / "qualify-selfupdate"
            source.write_bytes(b"offline fixture executable bytes")
            source.chmod(0o755)
            destination_parent = root / "usr-local-lib-minecraft-ui"
            destination_parent.mkdir(mode=0o755)
            destination_parent.chmod(0o755)
            destination = destination_parent / "qualification-updater"
            uid, gid = os.getuid(), os.getgid()

            staged = MODULE.install_qualification_helper(source, destination, uid, gid)
            info = staged.lstat()
            self.assertEqual(staged.read_bytes(), source.read_bytes())
            self.assertEqual((info.st_uid, info.st_gid, MODULE.stat.S_IMODE(info.st_mode), info.st_nlink),
                             (uid, gid, 0o755, 1))
            self.assertEqual(private_source_parent.stat().st_mode & 0o777, 0o700)
            with self.assertRaisesRegex(RuntimeError, "destination already exists"):
                MODULE.install_qualification_helper(source, destination, uid, gid)

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

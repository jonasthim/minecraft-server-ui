import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).with_name("linux_installer_diagnostics.py")
SPEC = importlib.util.spec_from_file_location("linux_installer_diagnostics", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class InstallerDiagnosticTests(unittest.TestCase):
    def test_protected_snapshot_reports_only_fixed_slot_metadata(self):
        with tempfile.TemporaryDirectory() as temp:
            base = Path(temp)
            state = base / "state"
            install = state / "install"
            binary = base / "bin"
            operation = install / ("a" * 64)
            operation.mkdir(parents=True)
            state.chmod(0o700)
            install.chmod(0o700)
            operation.chmod(0o700)
            binary.mkdir()
            data = base / "data"
            control = data / "selfupdate" / "control"
            control.mkdir(parents=True)
            secret = "token=do-not-report"
            (control / "maintenance-challenge.json").write_text(secret)
            (state / "installation-transaction.pending").write_text(secret)
            (state / "installation-launch.json").symlink_to(base / "outside-secret")
            (base / "outside-secret").write_text(secret)
            (operation / "result.json.pending").write_text(secret)
            (operation / "inputs").mkdir()
            (operation / "inputs" / "private-member-name").write_text(secret)
            (operation / "unexpected-secret-filename").write_text(secret)
            (binary / "minecraft-ui.previous").write_text(secret)
            (binary / "minecraft-ui.secret-suffix").write_text(secret)
            (binary / ("minecraft-ui.install-" + "a" * 64)).write_text(secret)

            snapshot = MODULE._protected_state_snapshot(str(state), str(install), str(binary), os.geteuid(), str(data))
            encoded = json.dumps(snapshot, sort_keys=True)

            self.assertEqual(snapshot["state"]["slots"]["installation-transaction.pending"]["kind"], "file")
            self.assertEqual(snapshot["state"]["slots"]["installation-launch.json"]["kind"], "symlink")
            self.assertEqual(snapshot["install"]["operations"][0]["slots"]["result.json.pending"]["state"], "present")
            self.assertEqual(snapshot["install"]["operations"][0]["slots"]["inputs"]["kind"], "directory")
            self.assertEqual(snapshot["install"]["operations"][0]["entries_state"], "present")
            self.assertEqual(snapshot["install"]["unknown_entries"], 1)
            self.assertEqual(snapshot["bin_scratch"]["unknown_minecraft_ui_entries"], 2)
            self.assertEqual(snapshot["operation_scratch"][0]["metadata"]["kind"], "file")
            self.assertEqual(snapshot["control_presence"]["slots"]["maintenance-challenge.json"], "present")
            self.assertEqual(snapshot["control_presence"]["slots"]["challenge.json"], "absent")
            for forbidden in (secret, "private-member-name", "unexpected-secret-filename", "minecraft-ui.secret-suffix"):
                self.assertNotIn(forbidden, encoded)

    def test_snapshot_refuses_symlink_directories_without_traversing_them(self):
        with tempfile.TemporaryDirectory() as temp:
            base = Path(temp)
            real = base / "real"
            real.mkdir()
            secret = real / "secret"
            secret.write_text("sensitive")
            state_link = base / "state-link"
            install_link = base / "install-link"
            os.symlink(real, state_link)
            os.symlink(real, install_link)
            binary = base / "bin"
            binary.mkdir()

            snapshot = MODULE._protected_state_snapshot(str(state_link), str(install_link), str(binary),
                                                        os.geteuid(), str(base / "missing-data"))

            self.assertEqual(snapshot["state"]["state"], "refused")
            self.assertEqual(snapshot["install"]["state"], "refused")
            self.assertNotIn("sensitive", repr(snapshot))

    def test_operation_fstat_failure_closes_descriptor_and_returns_snapshot(self):
        with tempfile.TemporaryDirectory() as temp:
            base = Path(temp)
            state = base / "state"
            install = state / "install"
            operation = install / ("c" * 64)
            operation.mkdir(parents=True)
            state.chmod(0o700)
            install.chmod(0o700)
            operation.chmod(0o700)
            binary = base / "bin"
            binary.mkdir()
            captured = []

            def fail_fstat(fd):
                captured.append(fd)
                raise OSError("fixture fstat failure")

            with patch.object(MODULE, "_operation_directory_info", side_effect=fail_fstat):
                snapshot = MODULE._protected_state_snapshot(str(state), str(install), str(binary),
                                                            os.geteuid(), str(base / "missing-data"))

            self.assertEqual(snapshot["install"]["operations"], [{"id": "c" * 64, "state": "refused"}])
            self.assertEqual(len(captured), 1)
            with self.assertRaises(OSError):
                os.fstat(captured[0])

    def test_snapshot_bounds_operation_and_entry_enumeration(self):
        with tempfile.TemporaryDirectory() as temp:
            base = Path(temp)
            state = base / "state"
            install = state / "install"
            binary = base / "bin"
            install.mkdir(parents=True)
            state.chmod(0o700)
            install.chmod(0o700)
            binary.mkdir()
            for value in range(20):
                operation = install / (format(value + 1, "064x"))
                operation.mkdir()
                operation.chmod(0o700)
                for entry in range(12):
                    (operation / ("unknown-" + str(entry))).touch()
            for value in range(80):
                (install / ("unknown-root-" + str(value))).touch()

            snapshot = MODULE._protected_state_snapshot(str(state), str(install), str(binary),
                                                        os.geteuid(), str(base / "missing-data"))

            self.assertLessEqual(len(snapshot["install"]["operations"]), MODULE.MAX_INSTALL_OPERATIONS)
            self.assertLessEqual(snapshot["install"]["unknown_entries"], MODULE.MAX_INSTALL_ENTRIES)
            self.assertTrue(snapshot["install"]["truncated"])
            self.assertNotIn("unknown-root-", repr(snapshot))
            self.assertNotIn("unknown-0", repr(snapshot))

    def test_payload_cap_omits_snapshot_after_adding_diagnostics(self):
        payload = {"format": 1, "scope": "fixed", "units": [],
                   "journal": {"bounded": True, "events": [], "worker_results": [], "entry_failures": []},
                   "protected_state": {"fixture": "x" * (MODULE.MAX_PAYLOAD_BYTES * 2)}}

        encoded = MODULE._bounded_payload(payload)

        self.assertLessEqual(len(encoded.encode("utf-8")), MODULE.MAX_PAYLOAD_BYTES)
        self.assertNotIn("protected_state", json.loads(encoded))

    def test_journal_scrubber_keeps_stage_and_omits_raw_message(self):
        summary = MODULE.summarize_journal([
            '{"_SYSTEMD_UNIT":"minecraft-ui-install-' + 'a' * 64 + '.service",'
            '"PRIORITY":"3","MESSAGE":"verifying challenge nonce=secret-value"}',
            '{"_SYSTEMD_UNIT":"unrelated.service","PRIORITY":"3","MESSAGE":"prepared"}',
        ])
        self.assertEqual(summary["events"], [{
            "unit": "minecraft-ui-install-" + "a" * 64 + ".service",
            "priority": "3", "stage": "verifying", "count": 1,
        }])
        self.assertNotIn("secret-value", repr(summary))

    def test_worker_result_preserves_allowlisted_failure_stage(self):
        operation_id = "b" * 64
        message = MODULE.json.dumps({
            "outcome": "manual_repair",
            "operation_id": operation_id,
            "failed_stage": "prepare",
            "image": {"version": "v1.2.3", "schema": 20, "sha256": "a" * 64},
        }, separators=(",", ":"))
        summary = MODULE.summarize_journal([
            MODULE.json.dumps({
                "_SYSTEMD_UNIT": "minecraft-ui-install-" + operation_id + ".service",
                "PRIORITY": "3",
                "MESSAGE": message,
            })
        ])

        self.assertEqual(summary["worker_results"], [{
            "unit": "minecraft-ui-install-" + operation_id + ".service",
            "outcome": "manual_repair",
            "stage": "prepare",
            "operation_id": operation_id,
            "image": {"version": "v1.2.3", "schema": 20, "sha256": "a" * 64},
            "count": 1,
        }])
        self.assertEqual(summary["events"], [{
            "unit": "minecraft-ui-install-" + operation_id + ".service",
            "priority": "3", "stage": "prepare", "count": 1,
        }])
        self.assertNotIn("MESSAGE", repr(summary))

    def test_worker_result_rejects_unknown_fields_and_secret_values(self):
        operation_id = "c" * 64
        message = MODULE.json.dumps({
            "outcome": "manual_repair",
            "operation_id": operation_id,
            "failed_stage": "recover",
            "token": "secret-token-value",
        }, separators=(",", ":"))
        summary = MODULE.summarize_journal([
            MODULE.json.dumps({
                "_SYSTEMD_UNIT": "minecraft-ui-install-" + operation_id + ".service",
                "PRIORITY": "3",
                "MESSAGE": message,
            })
        ])

        self.assertEqual(summary["worker_results"], [])
        self.assertNotIn("secret-token-value", repr(summary))

    def test_worker_result_rejects_duplicate_json_fields(self):
        operation_id = "d" * 64
        message = ('{"outcome":"manual_repair","outcome":"installed",'
                   '"operation_id":"' + operation_id + '","failed_stage":"prepare"}')
        summary = MODULE.summarize_journal([
            MODULE.json.dumps({
                "_SYSTEMD_UNIT": "minecraft-ui-install-" + operation_id + ".service",
                "PRIORITY": "3",
                "MESSAGE": message,
            })
        ])

        self.assertEqual(summary["worker_results"], [])

    def test_worker_result_must_match_install_unit_identity(self):
        operation_id = "e" * 64
        message = MODULE.json.dumps({
            "outcome": "manual_repair",
            "operation_id": "f" * 64,
            "failed_stage": "preflight",
        }, separators=(",", ":"))
        summary = MODULE.summarize_journal([
            MODULE.json.dumps({
                "_SYSTEMD_UNIT": "minecraft-ui-install-" + operation_id + ".service",
                "PRIORITY": "3",
                "MESSAGE": message,
            })
        ])

        self.assertEqual(summary["worker_results"], [])

    def test_exact_known_entry_failures_are_categorized_without_message_text(self):
        operation_id = "a" * 64
        messages = [
            ("native manager operation refused: installer worker could not acquire its protected session",
             "protected_session_unavailable", "preflight"),
            ("native manager operation refused: installer worker transaction does not match",
             "worker_transaction_mismatch", "recover"),
            ("native manager operation refused: installer worker executable is not authorized",
             "worker_executable_unauthorized", "preflight"),
            ("native manager operation refused: installer worker transaction is not prepared",
             "worker_transaction_not_prepared", "recover"),
        ]
        lines = [MODULE.json.dumps({
            "_SYSTEMD_UNIT": "minecraft-ui-install-" + operation_id + ".service",
            "PRIORITY": "3",
            "MESSAGE": message,
        }) for message, _, _ in messages]
        summary = MODULE.summarize_journal(lines)

        self.assertEqual(summary["entry_failures"], [
            {"unit": "minecraft-ui-install-" + operation_id + ".service",
             "category": category, "stage": stage, "count": 1}
            for _, category, stage in sorted(messages, key=lambda item: item[1])
        ])
        self.assertNotIn("protected session", repr(summary))
        self.assertNotIn("native manager operation refused", repr(summary))
        self.assertEqual({event["stage"] for event in summary["events"]}, {"preflight", "recover"})

    def test_entry_failure_classification_requires_exact_message(self):
        operation_id = "9" * 64
        summary = MODULE.summarize_journal([
            MODULE.json.dumps({
                "_SYSTEMD_UNIT": "minecraft-ui-install-" + operation_id + ".service",
                "PRIORITY": "3",
                "MESSAGE": ("native manager operation refused: installer worker transaction does not match "
                            "token=secret-value"),
            })
        ])

        self.assertEqual(summary["entry_failures"], [])
        self.assertNotIn("secret-value", repr(summary))

    def test_worker_results_sort_mixed_optional_stages(self):
        operation_id = "8" * 64
        unit = "minecraft-ui-install-" + operation_id + ".service"
        messages = [
            MODULE.json.dumps({"outcome": "manual_repair", "operation_id": operation_id}, separators=(",", ":")),
            MODULE.json.dumps({"outcome": "manual_repair", "operation_id": operation_id,
                               "failed_stage": "prepare"}, separators=(",", ":")),
        ]
        summary = MODULE.summarize_journal([
            MODULE.json.dumps({"_SYSTEMD_UNIT": unit, "PRIORITY": "3", "MESSAGE": message})
            for message in messages
        ])

        self.assertEqual([result["stage"] for result in summary["worker_results"]], ["prepare", None])

    def test_journal_scrubber_ignores_non_object_json(self):
        summary = MODULE.summarize_journal(["null", "[]", '"message"'])

        self.assertEqual(summary, {"bounded": True, "events": [], "worker_results": [], "entry_failures": []})


if __name__ == "__main__":
    unittest.main()

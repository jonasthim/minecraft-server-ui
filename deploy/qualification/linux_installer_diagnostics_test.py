import importlib.util
from pathlib import Path
import unittest


SCRIPT = Path(__file__).with_name("linux_installer_diagnostics.py")
SPEC = importlib.util.spec_from_file_location("linux_installer_diagnostics", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class InstallerDiagnosticTests(unittest.TestCase):
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

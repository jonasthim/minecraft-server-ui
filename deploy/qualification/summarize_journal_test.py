#!/usr/bin/env python3
"""Exercise the actual scrubber with fixed markers and private decoys."""
import json
from pathlib import Path
import subprocess
import sys
import unittest


class JournalSummaryTests(unittest.TestCase):
    def summarize(self, entries):
        result = subprocess.run(
            [sys.executable, str(Path(__file__).with_name("summarize-journal.py"))],
            input="\n".join(json.dumps(entry) for entry in entries),
            text=True, capture_output=True, check=True,
        )
        self.assertEqual(result.stderr, "")
        self.assertNotIn("private-secret", result.stdout)
        return json.loads(result.stdout)

    def test_fixed_activation_stage_is_retained(self):
        result = self.summarize([
            {"PRIORITY": "3", "MESSAGE": "native-update-failure:drain_read", "nonce": "private-secret"},
            {"PRIORITY": "3", "MESSAGE": "native-update-failure:drain_read"},
            {"PRIORITY": "3", "MESSAGE": "native-update-failure:schema_probe"},
        ])
        self.assertEqual(result.get("native_update_failure_counts"), {"drain_read": 2, "schema_probe": 1})
        self.assertEqual(set(result), {"priority_counts", "known_event_counts", "native_update_failure_counts", "native_schema_probe_counts", "native_pam_failure_counts"})

    def test_unknown_or_extended_marker_is_omitted(self):
        messages = [
            "native-update-failure:private-secret",
            "native-update-failure:drain_read private-secret",
            "private-secret native-update-failure:drain_read",
            "native-update-failure:schema_probe\nprivate-secret",
            {"stage": "private-secret"},
            None,
        ]
        result = self.summarize([{"MESSAGE": message, "PRIORITY": "3"} for message in messages])
        self.assertEqual(result.get("native_update_failure_counts"), {})
        self.assertEqual(result.get("native_schema_probe_counts"), {})
        self.assertEqual(result["known_event_counts"], {})

    def test_schema_probe_keeps_only_exact_fixed_and_bounded_numeric_markers(self):
        accepted = ["schema_read:sqlite:14", "actor_read:permission", "config:other", "child:entered",
                    "runuser_session:permission", "runuser_credentials:credentials", "runuser_exec:eacces",
                    "command:exit:37", "command:signal:15", "command:launch_not_exist",
                    "command:output_overflow", "command:stderr_overflow", "command:invalid_json",
                    "command:context_canceled", "command:context_deadline"]
        rejected = ["schema_read:sqlite:0", "schema_read:sqlite:65536", "schema_read:sqlite:01",
                    "schema_read:sqlite:+1", "schema_read:sqlite:-1", "command:exit:256",
                    "command:exit:0", "command:signal:65", "command:signal:-1", "command:signal:01",
                    "schema_read:private-secret", "private-secret:permission",
                    "command:exit:37 private-secret", "schema_read:sqlite:14\nprivate-secret",
                    "schema_read:permission:private-secret", "schema_read:sqlite:١٤",
                    "child:entered private-secret", "child:entered\nprivate-secret", "child:other",
                    "runuser_session:private-secret", "runuser_exec:permission", "runuser_exec:eacces\r"]
        entries = [{"PRIORITY": "3", "MESSAGE": "native-update-schema-diagnostic:" + marker}
                   for marker in accepted + rejected]
        result = self.summarize(entries)
        self.assertEqual(result.get("native_schema_probe_counts"), {marker: 1 for marker in accepted})
        self.assertEqual(result["known_event_counts"], {})

    def test_pam_operation_messages_require_exact_finite_grammar(self):
        accepted = {
            "pam_limits(runuser:session): Could not set limit for 'nice': Operation not permitted": "limits:nice:eperm",
            "pam_limits(runuser:session): Could not set limit for 'nofile': Permission denied": "limits:nofile:eacces",
            "pam_limits(runuser:session): Could not set limit for PRIO_PROCESS: Permission denied": "limits:priority:eacces",
            "pam_limits(runuser:session): Could not set prctl(PR_SET_NO_NEW_PRIVS): Invalid argument": "limits:nonewprivs:einval",
            "pam_unix(runuser:session): open_session - error recovering username": "unix:username:other",
        }
        rejected = [
            "pam_limits(runuser:session): Could not set limit for 'private-secret': Permission denied",
            "pam_limits(runuser:session): Could not set limit for 'nice': private-secret",
            "private-secret pam_limits(runuser:session): Could not set limit for 'nice': Operation not permitted",
            "pam_limits(runuser:session): Could not set limit for 'nice': Operation not permitted private-secret",
            "pam_limits(runuser:session): Could not set limit for 'nice': Operation not permitted\nprivate-secret",
            "pam_limits(private-secret:session): Could not set limit for 'nice': Operation not permitted",
        ]
        entries = [{"MESSAGE": message, "PRIORITY": "3"} for message in list(accepted) + rejected]
        result = self.summarize(entries)
        self.assertEqual(result.get("native_pam_failure_counts"), {value: 1 for value in accepted.values()})

    def test_existing_count_fields_are_preserved(self):
        result = self.summarize([
            {"PRIORITY": "6", "MESSAGE": "Minecraft manager listening private-secret"},
            {"PRIORITY": "invalid", "MESSAGE": "private-secret"},
        ])
        self.assertEqual(result["priority_counts"], {"6": 1, "unknown": 1})
        self.assertEqual(result["known_event_counts"], {"Minecraft manager listening": 1})
        self.assertEqual(result.get("native_update_failure_counts"), {})
        self.assertEqual(result.get("native_schema_probe_counts"), {})


if __name__ == "__main__":
    unittest.main()

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
        self.assertEqual(set(result), {"priority_counts", "known_event_counts", "native_update_failure_counts"})

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
        self.assertEqual(result["known_event_counts"], {})

    def test_existing_count_fields_are_preserved(self):
        result = self.summarize([
            {"PRIORITY": "6", "MESSAGE": "Minecraft manager listening private-secret"},
            {"PRIORITY": "invalid", "MESSAGE": "private-secret"},
        ])
        self.assertEqual(result["priority_counts"], {"6": 1, "unknown": 1})
        self.assertEqual(result["known_event_counts"], {"Minecraft manager listening": 1})
        self.assertEqual(result.get("native_update_failure_counts"), {})


if __name__ == "__main__":
    unittest.main()

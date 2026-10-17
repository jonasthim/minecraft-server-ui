#!/usr/bin/env python3
"""Ensure updater unit context is reduced to fixed metadata."""
import json
from pathlib import Path
import subprocess
import sys
import unittest


class UnitContextSummaryTests(unittest.TestCase):
    def summarize(self, raw):
        return subprocess.run(
            [sys.executable, str(Path(__file__).with_name("summarize-updater-unit.py"))],
            input=raw, text=True, capture_output=True, check=True,
        )

    def test_effective_instance_and_recovery_properties_are_summarized(self):
        raw = (
            "Id=minecraft-ui-update@run-abc.service\nUser=root\n"
            "CapabilityBoundingSet=CAP_CHOWN CAP_SETUID CAP_DAC_OVERRIDE\n"
            "PrivateUsers=no\nSystemCallFilter=~@mount @setuid\n"
            "RestrictSUIDSGID=yes\nNoNewPrivileges=yes\n\n"
            "Id=minecraft-ui-update-recovery.service\nUser=root\n"
            "CapabilityBoundingSet=\nPrivateUsers=no\nSystemCallFilter=\n"
            "RestrictSUIDSGID=yes\nNoNewPrivileges=yes\n"
        )
        result = self.summarize(raw)
        self.assertEqual(result.stderr, "")
        parsed = json.loads(result.stdout)
        self.assertEqual([unit["unit"] for unit in parsed["units"]], ["updater_instance", "recovery"])
        self.assertEqual(parsed["units"][0]["capability_bounding_set"], "configured")
        self.assertEqual(parsed["units"][0]["cap_setuid_named"], "yes")
        self.assertEqual(parsed["units"][1]["capability_bounding_set"], "empty")
        self.assertEqual(parsed["units"][1]["cap_setuid_named"], "no")
        self.assertNotIn("CAP_DAC_OVERRIDE", result.stdout)
        self.assertNotIn("@mount", result.stdout)

    def test_unknown_unit_and_duplicate_properties_do_not_retain_values(self):
        result = self.summarize(
            "Id=private-secret.service\nUser=/private/path\nUser=root\n"
            "CapabilityBoundingSet=CAP_SECRET\nPrivateUsers=maybe\n"
            "SystemCallFilter=private-secret\nRestrictSUIDSGID=maybe\nNoNewPrivileges=maybe\n"
        )
        self.assertNotIn("private-secret", result.stdout)
        self.assertNotIn("/private/path", result.stdout)
        parsed = json.loads(result.stdout)
        self.assertEqual(parsed["units"][0]["unit"], "unknown")
        self.assertEqual(parsed["units"][0]["user"], "unknown")
        self.assertEqual(parsed["units"][0]["private_users"], "unknown")

    def test_input_bound_returns_fixed_error(self):
        result = self.summarize("x" * (16 * 1024 + 1))
        self.assertEqual(json.loads(result.stdout), {"units": [], "error": "input_too_large"})


if __name__ == "__main__":
    unittest.main()

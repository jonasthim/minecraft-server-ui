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
            "CapabilityBoundingSet=cap_chown cap_setuid cap_dac_override\n"
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
        self.assertEqual(parsed["units"][0]["system_call_filter_mode"], "denylist")
        self.assertEqual(parsed["units"][0]["setuid_filter_named"], "yes")
        self.assertEqual(parsed["units"][1]["capability_bounding_set"], "empty")
        self.assertEqual(parsed["units"][1]["cap_setuid_named"], "no")
        self.assertNotIn("CAP_DAC_OVERRIDE", result.stdout)
        self.assertNotIn("@mount", result.stdout)

    def test_complemented_capabilities_and_syscall_filter_metadata(self):
        raw = (
            "Id=minecraft-ui-update@cap-a.service\nUser=root\n"
            "CapabilityBoundingSet=~cap_net_admin cap_setgid\nSystemCallFilter=~@mount\n\n"
            "Id=minecraft-ui-update@cap-b.service\nUser=root\n"
            "CapabilityBoundingSet=~cap_setuid\nSystemCallFilter=setuid @system-service\n"
        )
        parsed = json.loads(self.summarize(raw).stdout)["units"]
        self.assertEqual(parsed[0]["cap_setuid_named"], "yes")
        self.assertEqual(parsed[0]["setuid_filter_named"], "no")
        self.assertEqual(parsed[0]["system_call_filter_mode"], "denylist")
        self.assertEqual(parsed[1]["cap_setuid_named"], "no")
        self.assertEqual(parsed[1]["setuid_filter_named"], "yes")
        self.assertEqual(parsed[1]["system_call_filter_mode"], "allowlist")
        self.assertNotIn("cap_net_admin", self.summarize(raw).stdout)

    def test_filter_group_is_never_interpreted_as_containing_setuid(self):
        parsed = json.loads(self.summarize(
            "Id=minecraft-ui-update@filter.service\nSystemCallFilter=@system-service\n"
        ).stdout)["units"][0]
        self.assertEqual(parsed["system_call_filter_mode"], "allowlist")
        self.assertEqual(parsed["setuid_filter_named"], "no")
        self.assertNotIn("setuid_syscall_restricted", parsed)

    def test_invalid_mixed_polarity_is_unknown(self):
        parsed = json.loads(self.summarize(
            "Id=minecraft-ui-update@filter.service\nCapabilityBoundingSet=cap_chown ~cap_setuid\n"
            "SystemCallFilter=setuid ~@mount\n"
        ).stdout)["units"][0]
        self.assertEqual(parsed["cap_setuid_named"], "unknown")
        self.assertEqual(parsed["system_call_filter_mode"], "unknown")
        self.assertEqual(parsed["setuid_filter_named"], "unknown")

    def test_fixed_capability_probe_unit_names_are_classified(self):
        raw = (
            "Id=minecraft-ui-qualification-capability-baseline.service\nUser=root\n\n"
            "Id=minecraft-ui-qualification-capability-control.service\nUser=root\n"
        )
        units = json.loads(self.summarize(raw).stdout)["units"]
        self.assertEqual([unit["unit"] for unit in units], [
            "capability_probe_baseline", "capability_probe_control",
        ])

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

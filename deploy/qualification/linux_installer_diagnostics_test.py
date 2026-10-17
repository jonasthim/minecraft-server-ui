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


if __name__ == "__main__":
    unittest.main()

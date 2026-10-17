#!/usr/bin/env python3
"""Safe path-contract regression; never runs the native harness or installer.

Only the static files array, refusal loop, final absence loop, and fixed binary
cleanup operands are evaluated. Every absolute path is mapped under TemporaryDirectory
before shell/file operations; no service/account commands are extracted.
"""
import re
import shlex
import subprocess
import tempfile
import unittest
from pathlib import Path


HARNESS = Path(__file__).with_name("linux-updater.sh")
INSTALLER_SCRATCH = "/usr/local/bin/minecraft-ui.new"


class UpdaterPathsTest(unittest.TestCase):
    def setUp(self):
        self.source = HARNESS.read_text()
        array = re.search(r"^files=\(\n(.*?)^\)\n", self.source, re.M | re.S)
        self.assertIsNotNone(array, "static canonical path inventory missing")
        self.paths = shlex.split(array.group(1))
        self.assertTrue(all(path.startswith("/") and "$" not in path and ".." not in Path(path).parts for path in self.paths))
        self.guard = re.search(r'^for item in "\$\{files\[@\]\}"; do\n.*?^done$', self.source, re.M | re.S).group()
        self.final = re.search(r'^        for item in "\$\{files\[@\]\}"; do .*?; done$', self.source, re.M).group().strip()
        # Refusal must precede any ownership claim, which protects an existing
        # scratch file from the harness cleanup trap.
        self.assertLess(self.source.index(self.guard), self.source.index("owned=1"))
        self.temp = tempfile.TemporaryDirectory(prefix="updater-paths-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.scratch = self.map_path(INSTALLER_SCRATCH)
        self.scratch.parent.mkdir(parents=True)

    def map_path(self, path):
        return self.root / path.removeprefix("/")

    def run_fragment(self, fragment):
        paths = shlex.join(str(self.map_path(path)) for path in self.paths)
        return subprocess.run(["bash", "-c", "set -eu\nfiles=(" + paths + ")\nstatus=0\n" + fragment + '\nexit "$status"'],
                              capture_output=True, text=True, timeout=5)

    def test_refuse_existing_installer_scratch_regular_and_dangling_symlink(self):
        for kind in ("regular", "dangling-symlink"):
            with self.subTest(kind=kind):
                self.scratch.unlink(missing_ok=True)
                if kind == "regular":
                    self.scratch.write_bytes(b"preexisting installer bytes")
                else:
                    self.scratch.symlink_to(self.root / "absent-target")
                result = self.run_fragment(self.guard)
                self.assertEqual(result.returncode, 2, "preexisting installer scratch passed refusal")
                self.assertIn("Refusing existing canonical installation/state.", result.stderr)
                self.assertTrue(self.scratch.exists() or self.scratch.is_symlink(), "refusal removed unowned scratch")
                if kind == "regular":
                    self.assertEqual(self.scratch.read_bytes(), b"preexisting installer bytes")
                self.scratch.unlink()

    def test_cleanup_removes_owned_scratch_and_final_check_detects_leftovers(self):
        self.scratch.write_bytes(b"partial installer output")
        self.assertEqual(self.run_fragment(self.final).returncode, 1, "final check ignored installer scratch")
        removal = re.search(r"^        rm -f /usr/local/bin/minecraft-ui[^\n]*$", self.source, re.M).group()
        command = shlex.split(removal)
        self.assertEqual(command[:2], ["rm", "-f"])
        allowed = {"/usr/local/bin/minecraft-ui" + suffix for suffix in ("", ".new", ".next", ".previous")}
        self.assertTrue(set(command[2:]) <= allowed, "cleanup must retain static fixed operands")
        # The actual cleanup operand list is used, after containing each path.
        subprocess.run(command[:2] + [str(self.map_path(path)) for path in command[2:]], check=True, timeout=5)
        self.assertFalse(self.scratch.exists(), "owned installer scratch survived removal")
        self.assertEqual(self.run_fragment(self.final).returncode, 0)

    def test_empty_fixture_passes_refusal_and_final_absence(self):
        self.assertEqual(self.run_fragment(self.guard).returncode, 0)
        self.assertEqual(self.run_fragment(self.final).returncode, 0)


if __name__ == "__main__":
    unittest.main()

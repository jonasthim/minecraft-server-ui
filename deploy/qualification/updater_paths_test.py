#!/usr/bin/env python3
"""Safe path-contract regression; never runs the native harness or installer.

Only the static files array, refusal loop, final absence loop, and fixed binary
cleanup operands are evaluated. Every absolute path is mapped under TemporaryDirectory
before shell/file operations; no service/account commands are extracted.
"""
import re
import importlib.util
import json
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

    def prefix_functions(self):
        prefix = self.root / "borrowed-bin"
        prefix.mkdir()
        functions = []
        for name in ("prepare_hosted_prefix_mode", "restore_hosted_prefix"):
            body = re.search(r"^" + name + r"\(\) \{\n.*?^\}", self.source, re.M | re.S).group()
            functions.append(body.replace("/usr/local/bin", shlex.quote(str(prefix))))
        return prefix, "\n".join(functions)

    def test_borrowed_prefix_restores_exact_mode_and_preserves_other_bits(self):
        prefix, functions = self.prefix_functions()
        for original in (0o777, 0o2777, 0o1777, 0o755):
            with self.subTest(original=oct(original)):
                prefix.chmod(original)
                script = "set -eu\nborrowed_bin_prepared=0\n" + functions + "\nprepare_hosted_prefix_mode\n"
                script += 'printf "%s\\n" "$(stat -c %a ' + shlex.quote(str(prefix)) + ')"\nrestore_hosted_prefix\n'
                result = subprocess.run(["bash", "-c", script], capture_output=True, text=True, timeout=5)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(int(result.stdout.strip(), 8), original & ~0o22)
                self.assertEqual(prefix.stat().st_mode & 0o7777, original)

    def test_borrowed_prefix_restore_refuses_changed_directory_binding(self):
        prefix, functions = self.prefix_functions()
        target = self.root / "different-directory"
        target.mkdir(mode=0o777)
        target.chmod(0o777)
        script = "set -eu\nborrowed_bin_prepared=0\n" + functions + "\nprepare_hosted_prefix_mode\n"
        script += "mv " + shlex.quote(str(prefix)) + " " + shlex.quote(str(self.root / "old-binding")) + "\n"
        script += "ln -s " + shlex.quote(str(target)) + " " + shlex.quote(str(prefix)) + "\nrestore_hosted_prefix\n"
        result = subprocess.run(["bash", "-c", script], capture_output=True, text=True, timeout=5)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(target.stat().st_mode & 0o7777, 0o777, "restore chmod followed a replaced binding")

    def test_permission_diagnostics_emit_only_fixed_metadata(self):
        path = HARNESS.with_name("updater_permissions.py")
        spec = importlib.util.spec_from_file_location("updater_permissions", path)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        fixture = self.root / "private-fixture"
        fixture.write_text("credential-like fixture contents must never be reported")
        module.PATHS = (str(fixture), str(self.root / "missing"))
        report = module.snapshot("before-prefix-provisioning")
        self.assertEqual(report["paths"][0]["mode"], format(fixture.stat().st_mode & 0o7777, "o"))
        self.assertFalse(report["paths"][1]["exists"])
        self.assertNotIn(fixture.read_text(), json.dumps(report))
        allowed = {"path", "exists", "uid", "gid", "mode", "directory", "symlink", "metadata_read_failed"}
        self.assertTrue(all(set(record) <= allowed for record in report["paths"]))


if __name__ == "__main__":
    unittest.main()

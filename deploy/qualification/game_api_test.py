"""Network-free source checks; synthetic NBT/ZIP are not game qualification."""
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import struct
import tempfile
import re
import shlex
import subprocess
import unittest
from unittest import mock
import urllib.error
import zipfile

import game_api as game


def string(value):
    value = value.encode()
    return struct.pack(">H", len(value)) + value


def player(yaw):
    return gzip.compress(b"\x0a\0\0\x09" + string("Rotation") + b"\x05" + struct.pack(">iff", 2, yaw, 0) + b"\0")


class Response(io.BytesIO):
    def __init__(self, body, status=200, content="application/json"):
        from email.message import Message
        super().__init__(body)
        self.status = status
        self.headers = Message()
        self.headers["Content-Type"] = content


class Helpers(unittest.TestCase):
    def test_windows_failure_handler_does_not_resolve_unavailable_exception_type(self):
        # GitHub run39036770835 proved this type literal itself throws inside
        # catch, replacing the original dependency error. This source guard is
        # not a replacement for executing the catch path in native PowerShell.
        source = (game.HERE / "windows-game.ps1").read_text()
        self.assertNotIn("[Management.Automation.ParameterBindingValidationException]", source)

    def test_windows_identity_preserves_submicrosecond_creation_ticks(self):
        # Native FILETIME may end in any 100 ns tick; CIM roundtrips lose the
        # final decimal digit. Test the actual subprocess-result abstraction.
        ticks = 134100000000000007
        native = format(ticks, "016x")
        rounded = format(ticks - ticks % 10, "016x")
        self.assertNotEqual(native, rounded)
        identity = {"pid": 4321, "start_identity": "{00000000-0000-0000-0000-000000000001}:" + native}
        response = {"pid": 4321, "image": "test-java-image", "created": native}
        with mock.patch.object(game.subprocess, "run", return_value=mock.Mock(returncode=0, stdout=json.dumps(response).encode())) as call, mock.patch.object(game.os.path, "samefile", return_value=True):
            self.assertEqual(game.windows_native_identity(identity, Path("test-java-image")), native)
            script = call.call_args.args[0][-1]
            self.assertIn("GetProcessTimes", script)
            self.assertIn("OpenProcess", script)
            self.assertIn("GetProcessId(handle) != pid", script)
            self.assertIn("QueryFullProcessImageName(handle", script)
            self.assertIn('((uint)created.dwHighDateTime).ToString("x8") + ((uint)created.dwLowDateTime).ToString("x8")', script)
            self.assertNotIn("CreationDate", script)
        response["created"] = rounded
        with mock.patch.object(game.subprocess, "run", return_value=mock.Mock(returncode=0, stdout=json.dumps(response).encode())), mock.patch.object(game.os.path, "samefile", return_value=True):
            with self.assertRaises(game.Failure) as failure:
                game.windows_native_identity(identity, Path("test-java-image"))
            self.assertEqual(failure.exception.code, "native_start_mismatch")

    def test_windows_identity_rejects_wrong_pid_image_or_absent_process(self):
        identity = {"pid": 4321, "start_identity": "boot:01dc000000000007"}
        for response, same_image, returncode in (({"pid": 99, "image": "java", "created": "01dc000000000007"}, True, 0),
                                               ({"pid": 4321, "image": "foreign", "created": "01dc000000000007"}, False, 0),
                                               ({}, True, 1)):
            with self.subTest(response=response), mock.patch.object(game.subprocess, "run", return_value=mock.Mock(returncode=returncode, stdout=json.dumps(response).encode())), mock.patch.object(game.os.path, "samefile", return_value=same_image), self.assertRaises(game.Failure):
                game.windows_native_identity(identity, Path("java"))

    def test_hosted_only_requires_both_independent_signals(self):
        for env in ({}, {"GITHUB_ACTIONS": "true"}, {"RUNNER_ENVIRONMENT": "github-hosted"},
                    {"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "self-hosted"}):
            with mock.patch.dict(os.environ, env, clear=True), self.assertRaises(game.Failure):
                game.hosted()
        with mock.patch.dict(os.environ, {"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted"}, clear=True):
            game.hosted()

    def test_directory_and_identifier_boundaries(self):
        for identifier in ("../escape", "x/y", "x?foo", "x\n", 12, "a" * 65):
            with self.assertRaises(game.Failure):
                game.safe_id(identifier)
        self.assertEqual(game.safe_id("game-acceptance_1"), "game-acceptance_1")
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.assertEqual(game.safe_directory(tmp), root)
            (root / "commit.txt").write_text("a" * 40)
            self.assertEqual(game.safe_directory(tmp), root)
            (root / "anything").touch()
            with self.assertRaises(game.Failure):
                game.safe_directory(tmp)
            if os.name != "nt":
                (root / "alias").symlink_to(root, target_is_directory=True)
                with self.assertRaises(game.Failure):
                    game.safe_directory(str(root / "alias"))

    def test_nbt_reads_actual_typed_rotation_and_bounds_invalid_data(self):
        self.assertEqual(game.player_yaw(player(31.25)), 31.25)
        for raw in (b"not-gzip", gzip.compress(b"\x0a\0\0\x09" + string("Rotation") + b"\x05" + struct.pack(">i", 999999)),
                    gzip.compress(b"\x0a\0\0\0extra"), player(float("nan"))):
            with self.assertRaises((game.Failure, OSError)):
                game.player_yaw(raw)
        with self.assertRaises(game.Failure):
            game.player_yaw(gzip.compress(b"a" * ((1 << 20) + 1)))

    def test_yaw_requires_persisted_exact_marker(self):
        self.assertTrue(game.yaw_matches(391.25, 31.25))
        for value in (121.25, float("inf"), float("nan"), "31.25"):
            self.assertFalse(game.yaw_matches(value, 31.25))

    def test_permission_snapshot_emits_fixed_metadata_without_file_contents(self):
        with tempfile.TemporaryDirectory() as tmp:
            fixture = Path(tmp) / "private"
            fixture.write_text("secret fixture contents")
            with mock.patch.object(game, "PERMISSION_PATHS", (str(fixture), str(Path(tmp) / "missing"))):
                result = game.permission_snapshot("before-prefix-provisioning")
            self.assertEqual(result["paths"][0]["mode"], format(fixture.stat().st_mode & 0o7777, "o"))
            self.assertEqual(result["paths"][0]["inode"], fixture.stat().st_ino)
            self.assertFalse(result["paths"][1]["exists"])
            self.assertNotIn("secret", json.dumps(result))
            allowed = {"path", "exists", "uid", "gid", "mode", "device", "inode", "directory", "regular", "symlink", "metadata_read_failed"}
            self.assertTrue(all(set(entry) <= allowed for entry in result["paths"]))

    def test_api_csrf_origin_no_proxy_and_redacted_errors(self):
        api = game.API()
        api.csrf = "private-csrf"
        with mock.patch.object(api.client, "open", return_value=Response(b'{"ok":true}')) as call:
            self.assertEqual(api.request("/api/v1/instances", {}), {"ok": True})
            request = call.call_args.args[0]
            self.assertEqual(request.full_url, "http://127.0.0.1:8080/api/v1/instances")
            self.assertEqual(request.get_header("X-csrf-token"), "private-csrf")
            self.assertEqual(request.get_header("Origin"), game.BASE)
        body = b'{"error":{"code":"unsupported","message":"secret credential"}}'
        with mock.patch.object(api.client, "open", return_value=Response(body, 422)):
            with self.assertRaises(game.Failure) as failure:
                api.request("/api/v1/instances", {})
            self.assertEqual(failure.exception.evidence, {"http_status": 422, "api_code": "unsupported"})
            self.assertNotIn("secret", str(failure.exception))
        for path in ("https://attacker.invalid/", "/api/v1/x\r\nAuthorization: secret", "/api/v1/x\\evil"):
            with self.assertRaises(game.Failure):
                api.request(path)
        with self.assertRaises(game.Failure):
            game.NoRedirect().redirect_request(None, None, 302, "", {}, "https://elsewhere.invalid")

    def test_failed_job_emits_allowlisted_phase_without_raw_error(self):
        api = game.API()
        with mock.patch.object(api, "request", return_value={"job": {"id": "abc", "state": "failed", "phase": "verifying_backup", "error": "secret"}}):
            with self.assertRaises(game.Failure) as failure:
                api.job({"job": {"id": "abc"}})
            self.assertEqual(failure.exception.evidence, {"job_state": "failed", "job_phase": "verifying_backup", "job_diagnostics": ["unclassified"]})

    def test_job_diagnostics_classify_only_exact_public_authored_messages(self):
        api = game.API()
        cases = [
            ([{"message": "install preview expired or instance changed; resolve again"}], ["install_preview_stale"]),
            ([{"message": "Lifecycle operation failed; inspect its phase and current process state before retrying."}], ["lifecycle_failure_unspecified"]),
            ([{"message": "native systemd installation is missing or unsafe: /secret/token"}], ["unclassified"]),
            ([{"message": "https://private.invalid/password-secret"}], ["unclassified"]),
            ([{"message": "install preview expired or instance changed; resolve again secret"}], ["unclassified"]),
            ([{"message": 42}], ["unclassified"]),
            ([{"message": "Lifecycle managed java verification failed; inspect job phase and current process state before retrying."}], ["managed_java_verification_failed"]),
        ]
        for logs, expected in cases:
            with self.subTest(logs=logs), mock.patch.object(api, "request", return_value={"logs": logs}) as request:
                self.assertEqual(api.job_diagnostics("abc"), expected)
                self.assertEqual(request.call_args.args[0], "/api/v1/jobs/abc/logs?after=0&limit=500")
                self.assertNotIn("secret", json.dumps(api.job_diagnostics("abc")))
        with mock.patch.object(api, "request", side_effect=game.Failure("api_http_status", http_status=503)):
            self.assertEqual(api.job_diagnostics("abc"), ["diagnostics_unavailable"])
        with mock.patch.object(api, "request", return_value={"logs": [{"message": "ignored"}] * 501}):
            self.assertEqual(api.job_diagnostics("abc"), ["diagnostics_bound"])

    def test_archive_checks_hashes_player_marker_and_unsafe_paths(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "archive.zip"
            name = "world/playerdata/" + game.offline_uuid() + ".dat"
            files = {name: player(31.25), "world/level.dat": b"test-only-world"}
            def write(mutate=None, extra=None):
                manifest = {"instance_id": game.INSTANCE, "minecraft_version": game.VERSION, "kind": "full", "world_present": True,
                            "capture_mode": "acknowledged_native_save_lease", "files": [
                            {"path": n, "size": len(v), "sha256": hashlib.sha256(v).hexdigest()} for n, v in files.items()]}
                if mutate:
                    mutate(manifest)
                with zipfile.ZipFile(path, "w") as archive:
                    archive.writestr("backup-manifest.json", json.dumps(manifest))
                    for n, value in files.items():
                        archive.writestr("server/" + n, value)
                    if extra:
                        archive.writestr(extra, b"injected")
            write()
            self.assertEqual(game.archive_evidence(path, 31.25, True)["files_verified"], 2)
            with self.assertRaises(game.Failure):
                game.archive_evidence(path, 121.25, True)
            write(lambda m: m["files"][0].update(sha256="0" * 64))
            with self.assertRaises(game.Failure):
                game.archive_evidence(path, 31.25, True)
            for name in ("../escaped", "server/C:/escaped", "server\\escaped", "server/world/extra.dat"):
                write(extra=name)
                with self.assertRaises(game.Failure):
                    game.archive_evidence(path, 31.25, True)

    def test_no_ambient_host_execution_from_client_or_harness_cli(self):
        import subprocess
        harness = subprocess.run([os.sys.executable, str(game.HERE / "game_api.py"), "--minecraft-version", "1.21.9", "--output", "/unused"], capture_output=True,
                                 env={**os.environ, "GITHUB_ACTIONS": "false"}, timeout=10)
        self.assertEqual(harness.returncode, 2)
        self.assertIn(b"hosted_only", harness.stderr)
        invalid = subprocess.run([os.sys.executable, str(game.HERE / "game_api.py"), "--minecraft-version", "1.21.9;evil", "--output", "/unused"], capture_output=True, timeout=10)
        self.assertEqual(invalid.returncode, 2)
        client = subprocess.run(["node", str(game.HERE / "game-client/client.cjs"), "--mode", "observe"], capture_output=True,
                                env={**os.environ, "GITHUB_ACTIONS": "false"}, timeout=10)
        self.assertEqual(client.returncode, 2)
        self.assertEqual(client.stderr.strip(), b"client_hosted_only")


class LinuxGamePaths(unittest.TestCase):
    """Evaluate static path fragments only, remapped beneath a temp directory."""
    def setUp(self):
        self.source = (game.HERE / "linux-game.sh").read_text()
        guard = re.search(r"^for item in (.+); do\n.*?^done$", self.source, re.M | re.S)
        self.assertIsNotNone(guard)
        self.guard = guard.group()
        self.paths = shlex.split(guard.group(1))
        self.assertTrue(all(p.startswith("/") and "$" not in p and ".." not in Path(p).parts for p in self.paths))
        self.assertLess(self.source.index(self.guard), self.source.index("owned=1"))
        self.temp = tempfile.TemporaryDirectory(prefix="game-paths-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.scratch = self.map("/usr/local/bin/minecraft-ui.new")
        self.scratch.parent.mkdir(parents=True)

    def map(self, path):
        return self.root / path.removeprefix("/")

    def fragment(self, body):
        # Replace only static absolute operands after checking every operand.
        for path in sorted(set(self.paths + ["/usr/local/bin/minecraft-ui.new"]), key=len, reverse=True):
            body = re.sub(r"(?<!\S)" + re.escape(path) + r"(?=[ ;\n]|$)", lambda _: shlex.quote(str(self.map(path))), body)
        return subprocess.run(["bash", "-c", "set -eu\nstatus=0\n" + body + '\nexit "$status"'], capture_output=True, text=True, timeout=5)

    def test_existing_new_regular_and_dangling_symlink_are_preserved_on_refusal(self):
        for kind in ("regular", "dangling"):
            with self.subTest(kind=kind):
                self.scratch.unlink(missing_ok=True)
                if kind == "regular":
                    self.scratch.write_bytes(b"unowned installer scratch")
                else:
                    self.scratch.symlink_to(self.root / "absent")
                result = self.fragment(self.guard)
                self.assertEqual(result.returncode, 2)
                self.assertIn("FAIL existing_installation", result.stderr)
                self.assertTrue(self.scratch.exists() or self.scratch.is_symlink())
                if kind == "regular":
                    self.assertEqual(self.scratch.read_bytes(), b"unowned installer scratch")

    def test_owned_partial_new_is_removed_by_actual_cleanup_operands(self):
        self.scratch.write_bytes(b"owned partial installer bytes")
        removal = re.search(r"^            rm -f /etc/systemd/system/minecraft-ui.service[^\n]*$", self.source, re.M)
        self.assertIsNotNone(removal)
        command = shlex.split(removal.group())
        self.assertEqual(command[:2], ["rm", "-f"])
        self.assertTrue(set(command[2:]) <= set(self.paths))
        subprocess.run(command[:2] + [str(self.map(path)) for path in command[2:]], check=True, timeout=5)
        self.assertFalse(self.scratch.exists(), "owned .new partial output survived cleanup")

    def test_final_absence_check_covers_new_and_detects_leftover(self):
        final = re.search(r"^        for item in (.+); do \[\[ ! -e .*?; done$", self.source, re.M)
        self.assertIsNotNone(final, "final owned-state absence check missing")
        self.assertIn("/usr/local/bin/minecraft-ui.new", shlex.split(final.group(1)))
        self.scratch.write_bytes(b"leftover")
        self.assertEqual(self.fragment(final.group()).returncode, 1)
        self.scratch.unlink()
        self.assertEqual(self.fragment(final.group()).returncode, 0)

    def prefix_functions(self):
        prefix = self.root / "borrowed-bin"
        prefix.mkdir()
        functions = []
        for name in ("prepare_hosted_prefix_mode", "restore_hosted_prefix"):
            body = re.search(r"^" + name + r"\(\) \{\n.*?^\}", self.source, re.M | re.S)
            self.assertIsNotNone(body, "borrowed prefix function missing")
            functions.append(body.group().replace("/usr/local/bin", shlex.quote(str(prefix))))
        return prefix, "\n".join(functions)

    def test_borrowed_prefix_preserves_unrelated_bits_and_restores_exact_mode(self):
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

    def test_borrowed_prefix_restore_refuses_changed_inode_binding(self):
        prefix, functions = self.prefix_functions()
        target = self.root / "different-directory"
        target.mkdir(mode=0o777)
        target.chmod(0o777)
        script = "set -eu\nborrowed_bin_prepared=0\n" + functions + "\nprepare_hosted_prefix_mode\n"
        script += "mv " + shlex.quote(str(prefix)) + " " + shlex.quote(str(self.root / "old-binding")) + "\n"
        script += "ln -s " + shlex.quote(str(target)) + " " + shlex.quote(str(prefix)) + "\nrestore_hosted_prefix\n"
        result = subprocess.run(["bash", "-c", script], capture_output=True, text=True, timeout=5)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(target.stat().st_mode & 0o7777, 0o777)


if __name__ == "__main__":
    unittest.main()

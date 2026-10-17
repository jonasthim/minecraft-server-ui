"""Network-free source checks; synthetic NBT/ZIP are not game qualification."""
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import struct
import tempfile
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
            self.assertEqual(failure.exception.evidence, {"job_state": "failed", "job_phase": "verifying_backup"})

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


if __name__ == "__main__":
    unittest.main()

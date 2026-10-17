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
import shutil
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


def launcher_identity():
    return {"instance_id": game.INSTANCE, "pid": 1234, "launcher_pid": 5678,
            "start_identity": "00000000-0000-0000-0000-000000000001:12345",
            "executable": "/var/lib/minecraft-ui/runtimes/java", "working_dir": "/var/lib/minecraft-ui/instances/game-acceptance/server",
            "java_runtime_id": "temurin-21-linux-x64-" + "a" * 64,
            "java_checksum": {"algorithm": "sha256", "value": "a" * 64},
            "started_at": "2026-10-17T12:00:00Z"}


class Helpers(unittest.TestCase):
    def test_launcher_identity_accepts_exact_linux_and_native_windows_formats(self):
        identity = launcher_identity()
        self.assertTrue(game._valid_process_identity(identity))
        windows = {**identity, "start_identity": "{00000000-0000-0000-0000-000000000001}:01dc000000000007"}
        self.assertTrue(game._valid_process_identity(windows))
        for malformed in (
            "{00000000-0000-0000-0000-000000000001:01dc000000000007",
            "{00000000-0000-0000-0000-000000000001}:01dc00000000000",
            "{00000000-0000-0000-0000-000000000001}:01dc00000000000g",
            "{00000000-0000-0000-0000-000000000001}:01dc000000000007x",
            "00000000-0000-0000-0000-000000000001:01dc000000000007",
        ):
            with self.subTest(start_identity=malformed):
                self.assertFalse(game._valid_process_identity({**identity, "start_identity": malformed}))

    def test_windows_npm_arguments_survive_shim_reparse_scope(self):
        pwsh = os.environ.get("MINECRAFT_UI_TEST_PWSH") or shutil.which("pwsh")
        if not pwsh:
            self.skipTest("PowerShell unavailable; source fixture requires an explicit development tool or native CI pwsh")
        source = (game.HERE / "windows-game.ps1").read_text()
        dependency = source[source.index(" $stage = 'dependency_npm'"):source.index(" $stage = 'firewall'")]
        with tempfile.TemporaryDirectory(prefix="game-npm-scope-") as tmp:
            root = Path(tmp)
            caller = root / "caller with spaces"
            caller.mkdir()
            scratch = caller / "scratch"
            scratch.mkdir()
            (caller / "game-client").mkdir()
            (root / "helper.ps1").write_text("function harmless-helper { return 1 }\n")
            # Purpose-written reproduction of npm11.19's command re-evaluation,
            # without invoking npm/Node/installers or copying its implementation.
            (root / "npm.ps1").write_text(r'''$statement = $MyInvocation.Statement
$tokens = $null; $parseErrors = $null
$tree = [Management.Automation.Language.Parser]::ParseInput($statement,[ref]$tokens,[ref]$parseErrors)
$elements = $tree.EndBlock.Statements[0].PipelineElements[0].CommandElements
$argumentsText = ($elements | Select-Object -Skip 1 | ForEach-Object { $_.Extent.Text }) -join ' '
$capture = (Join-Path $PSScriptRoot 'capture.ps1').Replace("'", "''")
Invoke-Expression ("& '" + $capture + "' " + $argumentsText)
''')
            (root / "capture.ps1").write_text("@{argv=@($args)} | ConvertTo-Json -Compress\n")
            quote = lambda p: "'" + str(p).replace("'", "''") + "'"
            script = caller / "fixture.ps1"
            script.write_text("$ErrorActionPreference='Stop'\n"
                              ". " + quote(root / "helper.ps1") + "\n"
                              "$scratch=" + quote(scratch) + "\n"
                              "Set-Alias npm " + quote(root / "npm.ps1") + "\n"
                              "$LASTEXITCODE=0\n" + dependency)
            env = {**os.environ, "POWERSHELL_TELEMETRY_OPTOUT": "1", "XDG_CACHE_HOME": str(root / "cache"),
                   "XDG_CONFIG_HOME": str(root / "config"), "XDG_DATA_HOME": str(root / "data")}
            result = subprocess.run([pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-File", str(script)],
                                    capture_output=True, text=True, timeout=20, env=env)
            self.assertEqual(result.returncode, 0, "npm reparse-scope fixture failed: " + result.stderr)
            self.assertEqual(json.loads((scratch / "npm").read_text())["argv"],
                             ["ci", "--ignore-scripts", "--prefix", str(caller / "game-client")])

    def test_windows_failure_handler_does_not_resolve_unavailable_exception_type(self):
        # GitHub run39036770835 proved this type literal itself throws inside
        # catch, replacing the original dependency error. This source guard is
        # not a replacement for executing the catch path in native PowerShell.
        source = (game.HERE / "windows-game.ps1").read_text()
        self.assertNotIn("[Management.Automation.ParameterBindingValidationException]", source)

    def test_windows_cim_diagnostics_keep_only_bounded_numeric_allowlist(self):
        pwsh = os.environ.get("MINECRAFT_UI_TEST_PWSH") or shutil.which("pwsh")
        source = (game.HERE / "windows-game.ps1").read_text()
        function = source[source.index("function Get-BoundedCimNumericCodes"):source.index("$account = ")]
        self.assertIn("$exceptionTypeName -eq 'CimException'", source)
        self.assertNotIn(".Message", source)
        self.assertNotIn("RawErrorData", source)
        self.assertIn("@('NativeErrorCode','StatusCode','HResult')", function)
        if not pwsh:
            self.skipTest("PowerShell unavailable; numeric fixture requires explicit development tool or native CI pwsh")
        fixture = function + r'''
$sample = [pscustomobject]@{NativeErrorCode=[int]58;StatusCode=[System.DayOfWeek]::Monday;HResult=[int]-2147467259;Message='private'}
$sample | Add-Member -NotePropertyName Secret -NotePropertyValue 999
$result = Get-BoundedCimNumericCodes $sample
$result | ConvertTo-Json -Compress
'''
        with tempfile.TemporaryDirectory(prefix="game-cim-numbers-") as tmp:
            script = Path(tmp) / "fixture.ps1"
            script.write_text(fixture)
            result = subprocess.run([pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-File", str(script)],
                                    capture_output=True, text=True, timeout=20)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), {"NativeErrorCode": 58, "StatusCode": 1, "HResult": -2147467259})
        negative = function + r'''
$bad = [pscustomobject]@{NativeErrorCode='58';StatusCode=$true;HResult=[uint64]::MaxValue;Secret=[int]999}
Get-BoundedCimNumericCodes $bad | ConvertTo-Json -Compress
'''
        with tempfile.TemporaryDirectory(prefix="game-cim-negative-") as tmp:
            script = Path(tmp) / "fixture.ps1"
            script.write_text(negative)
            result = subprocess.run([pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-File", str(script)],
                                    capture_output=True, text=True, timeout=20)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), {})
        boundary = function + r'''
$ok = [pscustomobject]@{NativeErrorCode=[uint32]::MaxValue}
Get-BoundedCimNumericCodes $ok | ConvertTo-Json -Compress
'''
        with tempfile.TemporaryDirectory(prefix="game-cim-boundary-") as tmp:
            script = Path(tmp) / "fixture.ps1"
            script.write_text(boundary)
            result = subprocess.run([pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-File", str(script)],
                                    capture_output=True, text=True, timeout=20)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), {"NativeErrorCode": 4294967295})

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
            self.assertEqual(failure.exception.evidence, {"http_status": 422, "api_code": "unsupported", "api_operation": "instance_create"})
            self.assertNotIn("secret", str(failure.exception))
        for path in ("https://attacker.invalid/", "/api/v1/x\r\nAuthorization: secret", "/api/v1/x\\evil"):
            with self.assertRaises(game.Failure):
                api.request(path)
        with self.assertRaises(game.Failure):
            game.NoRedirect().redirect_request(None, None, 302, "", {}, "https://elsewhere.invalid")

    def test_http_failure_diagnostic_identifies_fixed_operation_without_path_or_payload(self):
        api = game.API()
        for path, payload, expected in ((game.PREFIX + "/backups", None, "backup_list"),
                                        (game.PREFIX + "/backups", {"kind": "full"}, "backup_create"),
                                        (game.PREFIX + "/console/logs?after=12&limit=500", None, "console_logs"),
                                        (game.PREFIX + "/backups/" + "a" * 32 + "/restore", {"confirmed": True}, "backup_restore"),
                                        ("/api/v1/unknown/private-secret", {"password": "private-secret"}, "unclassified")):
            with self.subTest(path=path), mock.patch.object(api.client, "open", return_value=Response(b'{"error":{"code":"instance_busy","message":"private-secret"}}', 409)):
                with self.assertRaises(game.Failure) as failure:
                    api.request(path, payload)
                self.assertEqual(failure.exception.evidence, {"http_status": 409, "api_code": "instance_busy", "api_operation": expected})
                self.assertNotIn("private-secret", json.dumps(failure.exception.evidence))

    def test_failed_job_emits_allowlisted_phase_without_raw_error(self):
        api = game.API()
        with mock.patch.object(api, "request", return_value={"job": {"id": "abc", "state": "failed", "phase": "verifying_backup", "error": "secret"}}):
            with self.assertRaises(game.Failure) as failure:
                api.job({"job": {"id": "abc"}})
            self.assertEqual(failure.exception.evidence, {"job_state": "failed", "job_phase": "verifying_backup", "job_diagnostics": ["unclassified"],
                                                         "console_diagnostics": {"diagnostic_available": False, "reason_code": "status_malformed"}})

    def test_stopping_job_adds_bounded_launcher_record_and_keeps_failure(self):
        api = game.API()
        response = {"job": {"id": "abc", "state": "failed", "phase": "stopping_process", "error": "private"}}
        diagnostic = {"diagnostic_available": False, "reason_code": "state_missing"}
        with mock.patch.object(api, "request", return_value=response), mock.patch.object(api, "job_diagnostics", return_value=["native_stop_unclean"]), mock.patch.object(api, "console_diagnostics", return_value={"diagnostic_available": False, "reason_code": "status_unavailable"}), mock.patch.object(game, "launcher_diagnostics", return_value=diagnostic), self.assertRaises(game.Failure) as failure:
            api.job({"job": {"id": "abc"}})
        self.assertEqual(failure.exception.evidence["job_state"], "failed")
        self.assertEqual(failure.exception.evidence["job_phase"], "stopping_process")
        self.assertEqual(failure.exception.evidence["launcher_diagnostics"], diagnostic)
        self.assertNotIn("private", json.dumps(failure.exception.evidence))

    def test_console_diagnostics_allow_only_known_reason_enums_and_actual_booleans(self):
        api = game.API()
        state = {"status": "settings_unverified", "reason": "native readiness status method is unavailable", "process_running": True,
                 "control_connected": False, "game_ready": False, "required_settings_verified": False, "ready_for_use": False,
                 "logs_available": True, "password": "private", "source": "private/path", "other": {"token": "private"}}
        with mock.patch.object(api, "request", return_value={"state": state}) as request:
            result = api.console_diagnostics()
            self.assertEqual(result, {"diagnostic_available": True, "status": "settings_unverified", "reason_code": "status_method_unavailable",
                                      "process_running": True, "control_connected": False, "game_ready": False, "required_settings_verified": False,
                                      "ready_for_use": False, "logs_available": True})
            self.assertEqual(request.call_args.args, (game.PREFIX + "/console/status",))
            self.assertEqual(request.call_args.kwargs, {"timeout": 5})
        state.update(status="private arbitrary value", reason="native readiness status method is unavailable secret-suffix",
                     process_running="true", control_connected=1, game_ready=None)
        with mock.patch.object(api, "request", return_value={"state": state}):
            result = api.console_diagnostics()
            self.assertEqual(result["status"], "unclassified")
            self.assertEqual(result["reason_code"], "unclassified")
            self.assertNotIn("process_running", result)
            self.assertNotIn("control_connected", result)
            self.assertNotIn("game_ready", result)
            self.assertNotIn("private", json.dumps(result))
            self.assertNotIn("secret", json.dumps(result))
        with mock.patch.object(api, "request", side_effect=game.Failure("api_http_status")):
            self.assertEqual(api.console_diagnostics(), {"diagnostic_available": False, "reason_code": "status_unavailable"})

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
            ([{"message": "Lifecycle process identity failed; inspect job phase and current process state before retrying."}], ["process_identity_failed"]),
            ([{"message": "Native stop did not complete cleanly; inspect current process state and launcher recovery details."}], ["native_stop_unclean"]),
            ([{"message": "Native stop did not complete cleanly; inspect current process state and launcher recovery details. private"}], ["unclassified"]),
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

    def test_launcher_diagnostics_match_fixed_identity_and_report_exit_or_unavailable(self):
        with tempfile.TemporaryDirectory(prefix="game-launcher-evidence-") as tmp:
            data = Path(tmp)
            state = data / "instances" / game.INSTANCE / "state"
            state.mkdir(parents=True)
            identity = launcher_identity()
            (state / "identity.json").write_text(json.dumps(identity))
            stop = {"identity": identity, "exited_at": "0001-01-01T00:00:00Z", "exit_code": 0,
                    "deliberate": True, "forced": False, "reason": "operator"}
            (state / "stop.json").write_text(json.dumps(stop))
            with mock.patch.object(game, "DATA", data):
                evidence = game.launcher_diagnostics()
                self.assertEqual(evidence, {"diagnostic_available": True, "identity_match": True,
                                            "exit_present": False,
                                            "exited_at_present": False, "deliberate": True, "forced": False,
                                            "watchdog_present": False, "watchdog_diagnostics": [],
                                            "exit_code": None, "reason_code": "operator"})
                exited = {"identity": identity, "exited_at": "2026-10-17T12:00:00Z", "exit_code": 0,
                          "deliberate": True, "forced": True, "reason": "operator", "watchdog_error": "secret"}
                (state / "exit.json").write_text(json.dumps(exited))
                evidence = game.launcher_diagnostics()
                self.assertTrue(evidence["identity_match"])
                self.assertTrue(evidence["exit_present"])
                self.assertTrue(evidence["exited_at_present"])
                self.assertTrue(evidence["forced"])
                self.assertTrue(evidence["watchdog_present"])
                self.assertEqual(evidence["exit_code"], 0)
                self.assertNotIn("secret", json.dumps(evidence))
                exited["identity"] = {**identity, "pid": 99}
                (state / "exit.json").write_text(json.dumps(exited))
                self.assertFalse(game.launcher_diagnostics()["identity_match"])
                exited["identity"] = identity
                exited["exited_at"] = "0001-01-01T00:00:00Z"
                (state / "exit.json").write_text(json.dumps(exited))
                evidence = game.launcher_diagnostics()
                self.assertTrue(evidence["exit_present"])
                self.assertFalse(evidence["exited_at_present"])
                (state / "exit.json").write_text("{malformed")
                unavailable = game.launcher_diagnostics()
                self.assertEqual(unavailable, {"diagnostic_available": False, "reason_code": "state_unavailable"})

    def test_launcher_diagnostics_reject_missing_typed_identity_fields_and_booleans(self):
        with tempfile.TemporaryDirectory(prefix="game-launcher-invalid-") as tmp:
            data = Path(tmp)
            state = data / "instances" / game.INSTANCE / "state"
            state.mkdir(parents=True)
            good = launcher_identity()
            (state / "identity.json").write_text(json.dumps(good))
            stop = {"identity": good, "exited_at": "0001-01-01T00:00:00Z", "exit_code": 0,
                    "deliberate": True, "forced": False, "reason": "manager_eof"}
            (state / "stop.json").write_text(json.dumps(stop))
            with mock.patch.object(game, "DATA", data):
                for identity in ({}, {**good, "instance_id": "other"}, {**good, "pid": 0},
                                 {**good, "pid": True}, {**good, "start_identity": ""},
                                 {**good, "start_identity": "bad:tick"},
                                 {**good, "start_identity": "{00000000-0000-0000-0000-000000000001:0000000000000001"},
                                 {**good, "start_identity": "{00000000-0000-0000-0000-000000000001}:000000000000001"},
                                 {**good, "start_identity": "{00000000-0000-0000-0000-000000000001}:000000000000000g"},
                                 {**good, "start_identity": "{00000000-0000-0000-0000-000000000001}:0000000000000001x"},
                                 {**good, "start_identity": "00000000-0000-0000-0000-000000000001:12345\n"}):
                    with self.subTest(identity=identity):
                        (state / "identity.json").write_text(json.dumps(identity))
                        self.assertFalse(game.launcher_diagnostics()["diagnostic_available"])
                (state / "identity.json").write_text(json.dumps(good))
                for record, filename in (({"identity": good, "exited_at": "0001-01-01T00:00:00Z", "exit_code": 0,
                                            "forced": False, "reason": "operator"}, "stop.json"),
                                         ({"identity": good, "exited_at": "0001-01-01T00:00:00Z", "exit_code": 0,
                                           "deliberate": "false", "forced": False}, "stop.json"),
                                         ({"identity": good, "exited_at": "0001-01-01T00:00:00Z", "exit_code": 0,
                                           "deliberate": True, "forced": 0}, "stop.json"),
                                         ({"identity": good, "exited_at": "0001-01-01T00:00:00Z", "exit_code": 0,
                                           "deliberate": True, "forced": False, "watchdog_error": None}, "stop.json"),
                                         ({"identity": good, "exited_at": "2026-10-17T12:00:00Z", "exit_code": 0,
                                           "deliberate": True}, "exit.json")):
                    (state / filename).write_text(json.dumps(record))
                    self.assertFalse(game.launcher_diagnostics()["diagnostic_available"])
                (state / "exit.json").write_text("{}")
                (state / "stop.json").write_text(json.dumps(stop))
                self.assertFalse(game.launcher_diagnostics()["diagnostic_available"])
                (state / "exit.json").write_bytes(b"x" * 32769)
                self.assertFalse(game.launcher_diagnostics()["diagnostic_available"])

    def test_launcher_watchdog_and_reason_categories_are_finite_and_exact(self):
        identity = launcher_identity()
        stop = {"identity": identity, "exited_at": "0001-01-01T00:00:00Z", "exit_code": 0,
                "deliberate": True, "forced": False, "reason": "manager_eof"}
        cases = (("stop delivery failed", ["stop_delivery_failed"]),
                 ("persist stop intention failed", ["stop_intention_persist_failed"]),
                 ("forced termination failed", ["forced_termination_failed"]),
                 ("stop delivery failed; forced termination failed", ["forced_termination_failed", "stop_delivery_failed"]),
                 ("stop delivery failed; unknown secret", ["unclassified"]),
                 ("stop delivery failed;forced termination failed", ["unclassified"]))
        for raw, expected in cases:
            with self.subTest(raw=raw), tempfile.TemporaryDirectory(prefix="game-launcher-watchdog-") as tmp:
                data = Path(tmp)
                state = data / "instances" / game.INSTANCE / "state"
                state.mkdir(parents=True)
                (state / "identity.json").write_text(json.dumps(identity))
                (state / "stop.json").write_text(json.dumps({**stop, "watchdog_error": raw}))
                with mock.patch.object(game, "DATA", data):
                    evidence = game.launcher_diagnostics()
                self.assertTrue(evidence["diagnostic_available"])
                self.assertEqual(evidence["reason_code"], "manager_eof")
                self.assertTrue(evidence["watchdog_present"])
                self.assertEqual(evidence["watchdog_diagnostics"], expected)
                self.assertNotIn("secret", json.dumps(evidence))

    def test_launcher_diagnostic_file_failure_does_not_replace_original_job_failure(self):
        api = game.API()
        response = {"job": {"id": "abc", "state": "failed", "phase": "stopping_process", "error": "private"}}
        with tempfile.TemporaryDirectory(prefix="game-launcher-oversize-") as tmp:
            data = Path(tmp)
            state = data / "instances" / game.INSTANCE / "state"
            state.mkdir(parents=True)
            (state / "identity.json").write_bytes(b"{}" + b" " * 32767)
            (state / "stop.json").write_text("{}")
            with mock.patch.object(game, "DATA", data), mock.patch.object(api, "request", return_value=response), \
                 mock.patch.object(api, "job_diagnostics", return_value=["unclassified"]), \
                 mock.patch.object(api, "console_diagnostics", return_value={"diagnostic_available": False, "reason_code": "status_unavailable"}), \
                 self.assertRaises(game.Failure) as failure:
                api.job({"job": {"id": "abc"}})
        self.assertEqual(failure.exception.code, "job_terminal_failure")
        self.assertEqual(failure.exception.evidence["job_phase"], "stopping_process")
        self.assertEqual(failure.exception.evidence["launcher_diagnostics"]["diagnostic_available"], False)
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

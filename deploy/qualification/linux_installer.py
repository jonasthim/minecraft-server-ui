#!/usr/bin/env python3
"""Drive the shipping Linux installer on a fresh GitHub-hosted Ubuntu VM."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import time
import urllib.error
import urllib.request


REPO = "jonasthim/minecraft-server-ui"
HEX64 = re.compile(r"[0-9a-f]{64}\Z")
PHASES = {"prepared", "quiescing", "stopped", "replacing", "published", "verifying", "rolling_back", "rolled_back", "committed", "manual_repair"}
INSTALLER_OUTCOMES = {"pending", "unchanged", "installed", "uninstalled", "rolled_back", "manual_repair"}
INSTALLER_FAILED_STAGES = {
    "preflight", "prepare", "record_prepared", "quiesce", "record_quiescing",
    "record_stopped", "record_replacing", "replace", "publish", "record_published",
    "record_verifying", "verify", "record_committed", "finish", "record_rollback",
    "rollback", "record_rolled_back", "record_manual_repair", "recover", "worker",
}
INSTALLER_RESULT_FIELDS = {"outcome", "operation_id", "image", "failed_stage"}
CHECK_STATES = {"absent", "recovery_required", "drifted", "installed", "uninstalled", "retained"}


def strict_json(raw):
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise ValueError("duplicate JSON member")
            result[key] = value
        return result
    return json.loads(raw, object_pairs_hook=pairs)


def valid_check_report(report, state, coherent, pending):
    allowed = {"state", "coherent", "pending", "image"}
    if not isinstance(report, dict) or set(report) - allowed:
        return False
    if report.get("state") != state or report.get("coherent") is not coherent or report.get("pending") is not pending:
        return False
    image = report.get("image")
    if image is None:
        return True
    if not isinstance(image, dict) or set(image) != {"version", "schema", "sha256"}:
        return False
    return (isinstance(image["version"], str)
            and re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", image["version"]) is not None
            and type(image["schema"]) is int and image["schema"] > 0
            and HEX64.fullmatch(image["sha256"]) is not None)


def safe_installer_result(stdout, return_code):
    """Return only validated, non-secret fields from a shipping CLI result."""
    diagnostic = {"return_code": return_code, "result": "unavailable"}
    try:
        lines = stdout.decode("utf-8", "strict").splitlines()
        if not lines:
            return diagnostic
        value = strict_json(lines[-1])
        if not isinstance(value, dict) or set(value) - INSTALLER_RESULT_FIELDS:
            return diagnostic
        if value.get("outcome") not in INSTALLER_OUTCOMES:
            return diagnostic
        safe = {"outcome": value["outcome"]}
        operation_id = value.get("operation_id")
        if operation_id is not None:
            if not isinstance(operation_id, str) or HEX64.fullmatch(operation_id) is None:
                return diagnostic
            safe["operation_id"] = operation_id
        failed_stage = value.get("failed_stage")
        if failed_stage is not None:
            if not isinstance(failed_stage, str) or failed_stage not in INSTALLER_FAILED_STAGES:
                return diagnostic
            safe["failed_stage"] = failed_stage
        image = value.get("image")
        if image is not None:
            if (not isinstance(image, dict) or set(image) != {"version", "schema", "sha256"}
                    or not isinstance(image["version"], str)
                    or re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", image["version"]) is None
                    or type(image["schema"]) is not int or image["schema"] <= 0
                    or not isinstance(image["sha256"], str) or HEX64.fullmatch(image["sha256"]) is None):
                return diagnostic
            safe["image"] = {"version": image["version"], "schema": image["schema"],
                             "sha256": image["sha256"]}
        diagnostic["result"] = safe
    except (UnicodeDecodeError, ValueError, TypeError):
        pass
    return diagnostic


def safe_check_report(stdout):
    """Return a normalized install-check report, rejecting unknown or unsafe data."""
    try:
        lines = stdout.decode("utf-8", "strict").splitlines()
        if not lines:
            return "unavailable"
        value = strict_json(lines[-1])
        if not isinstance(value, dict) or set(value) - {"state", "coherent", "pending", "image"}:
            return "unavailable"
        if (set(value) - {"image"} != {"state", "coherent", "pending"}
                or value["state"] not in CHECK_STATES
                or type(value["coherent"]) is not bool
                or type(value["pending"]) is not bool):
            return "unavailable"
        if value["state"] == "retained":
            if value["coherent"] or value["pending"]:
                return "unavailable"
        elif value["pending"] != (value["state"] == "recovery_required") or (value["coherent"] and value["pending"]):
            return "unavailable"
        image = value.get("image")
        if image is not None:
            if (not isinstance(image, dict) or set(image) != {"version", "schema", "sha256"}
                    or not isinstance(image["version"], str)
                    or re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", image["version"]) is None
                    or type(image["schema"]) is not int or image["schema"] <= 0
                    or not isinstance(image["sha256"], str) or HEX64.fullmatch(image["sha256"]) is None):
                return "unavailable"
            image = {"version": image["version"], "schema": image["schema"], "sha256": image["sha256"]}
        return {"state": value["state"], "coherent": value["coherent"],
                "pending": value["pending"], "image": image}
    except (UnicodeDecodeError, ValueError, TypeError):
        return "unavailable"


def failure_source_lines(error):
    """Return bounded line numbers for frames owned by this harness only."""
    lines = []
    traceback = error.__traceback__
    while traceback is not None:
        if traceback.tb_frame.f_code.co_filename == __file__:
            lines.append(traceback.tb_lineno)
        traceback = traceback.tb_next
    return lines[-16:]


def failure_source_line(error):
    """Return the last line from this harness, without exposing traceback text."""
    lines = failure_source_lines(error)
    return lines[-1] if lines else None


class Qualification:
    def __init__(self, bootstrap, candidates, output, scenario, qualifier=None, linux_inputs=None, windows_inputs=None):
        self.bootstrap = Path(bootstrap)
        self.candidates = Path(candidates)
        self.output = Path(output)
        self.repo = Path(__file__).resolve().parents[2]
        self.scenario = scenario
        self.qualifier = Path(qualifier) if qualifier else None
        self.linux_inputs = Path(linux_inputs) if linux_inputs else None
        self.windows_inputs = Path(windows_inputs) if windows_inputs else None
        self.checks = []
        self.prepare_attempt = 0
        self.last_command_result = {"return_code": None, "result": "unavailable"}
        self.scratch = Path("/run") / ("minecraft-ui-installer-qualification-" + str(os.getpid()))

    def record(self, name, evidence):
        self.checks.append({"case": name, "result": "pass", **evidence})

    def command(self, argv, expected=None, timeout=45):
        self.last_command_result = {"return_code": None, "result": "unavailable"}
        result = subprocess.run([str(x) for x in argv], stdin=subprocess.DEVNULL,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                timeout=timeout, check=False)
        self.last_command_result = {"return_code": result.returncode, "result": "unavailable"}
        if len(result.stdout) > 16384 or len(result.stderr) > 16384:
            raise RuntimeError("bounded command output exceeded")
        self.last_command_result = safe_installer_result(result.stdout, result.returncode)
        if len(argv) >= 2 and str(argv[-2:][0]) == "install" and str(argv[-1]) == "check":
            self.last_command_result["check_report"] = safe_check_report(result.stdout)
        if expected is not None and result.returncode != expected:
            raise RuntimeError("shipping command returned an unexpected status")
        return result

    def cli(self, *args, expected=0, timeout=45):
        return self.command([self.bootstrap, *args], expected=expected, timeout=timeout)

    def json_result(self, result):
        lines = result.stdout.decode("utf-8", "strict").splitlines()
        if not lines:
            raise RuntimeError("shipping command returned no structured result")
        value = strict_json(lines[-1])
        if not isinstance(value, dict):
            raise RuntimeError("shipping command result was not an object")
        return value

    def check(self, state, coherent, pending=False):
        result = self.cli("install", "check", timeout=20)
        report = self.json_result(result)
        if not valid_check_report(report, state, coherent, pending):
            raise RuntimeError("installer check state did not match the expected lifecycle state")
        return report

    def prepare_install(self, version, expected_outcome="installed", expected_exit=0):
        attempt = self.prepare_attempt
        self.prepare_attempt += 1
        destination = self.scratch / ("prepared-" + version.replace(".", "-") + "-" + str(attempt))
        candidate = self.candidates / version
        trust = candidate / "development-trust.json"
        result = self.cli("prepare", "--repository", REPO, "--trust-file", trust,
                          "--version", version, "--artifacts", candidate,
                          "--output", destination, "--allow-development", "--install",
                          expected=expected_exit, timeout=14 * 60)
        response = self.json_result(result)
        if response.get("outcome") != expected_outcome:
            raise RuntimeError("installer returned an unexpected terminal outcome")
        return response

    def absent_preflight_and_auth_failure(self):
        paths = [Path("/var/lib/minecraft-ui"), Path("/var/lib/minecraft-ui-update"),
                 Path("/etc/minecraft-ui"), Path("/usr/local/bin/minecraft-ui"),
                 Path("/usr/local/bin/minecraft-ui.new"), Path("/usr/local/bin/minecraft-ui.next"),
                 Path("/usr/local/bin/minecraft-ui.previous"), Path("/usr/local/lib/minecraft-ui"),
                 Path("/etc/sudoers.d/minecraft-ui"),
                 Path("/etc/systemd/system/minecraft-ui.service"),
                 Path("/etc/systemd/system/minecraft@.service"),
                 Path("/etc/systemd/system/minecraft-ui-update@.service"),
                 Path("/etc/systemd/system/minecraft-ui-update-recovery.service"),
                 Path("/etc/systemd/system/minecraft-ui.service.d"),
                 Path("/etc/systemd/system/minecraft-ui-qualification-independent.service")]
        before = {str(path): lstat_identity(path) for path in paths}
        self.check("absent", False)
        after = {str(path): lstat_identity(path) for path in paths}
        if before != after or any(value is not None for value in after.values()):
            raise RuntimeError("absent installer check changed canonical host state")
        source_candidate = self.candidates / "v0.0.1"
        tampered = self.scratch / "tampered-candidate"
        tampered.mkdir(mode=0o700)
        for name in ("bootstrap.json", "bootstrap.sig", "development-trust.json"):
            shutil.copyfile(source_candidate / name, tampered / name)
        signature = tampered / "bootstrap.sig"
        signed = bytearray(signature.read_bytes())
        marker = b'"signature":"'
        offset = signed.index(marker) + len(marker)
        signed[offset] = ord("A") if signed[offset] != ord("A") else ord("B")
        signature.write_bytes(signed)
        prepared = self.scratch / "auth-failure-output"
        failed = self.cli("prepare", "--repository", REPO, "--trust-file", tampered / "development-trust.json",
                          "--version", "v0.0.1", "--artifacts", tampered,
                          "--output", prepared, "--allow-development", "--install", expected=1)
        if prepared.exists() or failed.returncode == 0:
            raise RuntimeError("authenticated preparation failure was not effect-free")
        self.check("absent", False)
        if account_exists() or any(path.exists() for path in paths):
            raise RuntimeError("failed authentication created installation state")
        self.record("absent-read-only-and-authentication-refusal", {"created_state": False})

    def fresh_install_and_unchanged(self):
        response = self.prepare_install("v0.0.1")
        if response.get("failed_stage"):
            raise RuntimeError("fresh installation completed with a failed stage")
        before = manager_identity()
        if before["version"] != "v0.0.1":
            raise RuntimeError("fresh manager image did not match candidate v0.0.1")
        self.check("installed", True)
        self.health()
        update_trust_disabled()
        data_marker = Path("/var/lib/minecraft-ui/qualification-data.marker")
        data_marker.write_bytes(b"installer qualification data marker v1\n")
        os.chown(data_marker, account_ids()[0], account_ids()[1])
        os.chmod(data_marker, 0o600)
        world_marker = Path("/var/lib/minecraft-ui/instances/installer-fixture/server/world/qualification-world.marker")
        create_world_marker(world_marker)
        env_path = Path("/etc/minecraft-ui/environment")
        env_path.write_text("# qualification marker\nMINECRAFT_UI_BASE_URL=http://127.0.0.1:8080\n", encoding="utf-8")
        os.chown(env_path, 0, 0)
        os.chmod(env_path, 0o600)
        trust = fingerprint(Path("/etc/minecraft-ui/bootstrap-trust.json"))
        data = fingerprint(data_marker)
        world = fingerprint(world_marker)
        config = fingerprint(env_path)
        database = Path("/var/lib/minecraft-ui/manager.db")
        before_db = fingerprint(database)
        sidecars = {str(p): lstat_identity(p) for p in [database.with_name("manager.db-wal"), database.with_name("manager.db-shm")]}
        same = self.prepare_install("v0.0.1", expected_outcome="unchanged")
        after = manager_identity()
        after_db = fingerprint(database)
        self.check("installed", True)
        after_db_check = fingerprint(database)
        after_sidecars = {str(p): lstat_identity(p) for p in [database.with_name("manager.db-wal"), database.with_name("manager.db-shm")]}
        if before != after or before_db != after_db or after_db != after_db_check or sidecars != after_sidecars:
            raise RuntimeError("unchanged install or read-only check changed live manager identity or SQLite files")
        if fingerprint(data_marker) != data or fingerprint(world_marker) != world or fingerprint(env_path) != config or fingerprint(Path("/etc/minecraft-ui/bootstrap-trust.json")) != trust:
            raise RuntimeError("unchanged install changed retained fixture markers")
        if same.get("outcome") != "unchanged":
            raise RuntimeError("identical installation was not reported unchanged")
        self.record("fresh-install-and-unchanged-rerun", {"version": "v0.0.1", "manager_pid_preserved": True, "sqlite_identity_preserved": True})

    def game_fixture(self, start):
        dropin_dir = Path("/etc/systemd/system/minecraft@installer-fixture.service.d")
        dropin = dropin_dir / "override.conf"
        if start:
            dropin_dir.mkdir(mode=0o755)
            dropin.write_text("[Unit]\nAfter=\nWants=\n[Service]\nExecStart=\nExecStart=/usr/bin/sleep infinity\nWorkingDirectory=/var/lib/minecraft-ui\nTemporaryFileSystem=\nBindReadOnlyPaths=\nBindPaths=\nReadOnlyPaths=\nPrivateUsers=no\n", encoding="utf-8")
            os.chown(dropin_dir, 0, 0)
            os.chown(dropin, 0, 0)
            os.chmod(dropin, 0o644)
            self.command(["systemctl", "daemon-reload"], expected=0)
            self.command(["systemctl", "start", "minecraft@installer-fixture.service"], expected=0, timeout=60)
        else:
            self.command(["systemctl", "stop", "minecraft@installer-fixture.service"], expected=0, timeout=60)
        pid = int(self.command(["systemctl", "show", "--property=MainPID", "--value", "minecraft@installer-fixture.service"], expected=0).stdout.strip())
        return pid

    def upgrade_while_game_active(self):
        game_pid = self.game_fixture(True)
        dropin = Path("/etc/systemd/system/minecraft@installer-fixture.service.d/override.conf")
        identity = fingerprint(dropin)
        data = fingerprint(Path("/var/lib/minecraft-ui/qualification-data.marker"))
        world = fingerprint(Path("/var/lib/minecraft-ui/instances/installer-fixture/server/world/qualification-world.marker"))
        config = fingerprint(Path("/etc/minecraft-ui/environment"))
        trust = fingerprint(Path("/etc/minecraft-ui/bootstrap-trust.json"))
        prior_manager = manager_identity()
        self.prepare_install("v0.0.2")
        current = manager_identity()
        after_game = self.game_fixture_pid()
        if current["version"] != "v0.0.2" or (current["pid"], current["starttime"]) == (prior_manager["pid"], prior_manager["starttime"]) or after_game != game_pid:
            raise RuntimeError("upgrade did not replace only the manager process")
        if fingerprint(dropin) != identity or fingerprint(Path("/var/lib/minecraft-ui/qualification-data.marker")) != data or fingerprint(Path("/var/lib/minecraft-ui/instances/installer-fixture/server/world/qualification-world.marker")) != world or fingerprint(Path("/etc/minecraft-ui/environment")) != config or fingerprint(Path("/etc/minecraft-ui/bootstrap-trust.json")) != trust:
            raise RuntimeError("upgrade changed independent game fixture or retained data/config/trust")
        self.health()
        self.check("installed", True)
        self.record("v2-upgrade-with-independent-game-unit", {"version": "v0.0.2", "game_pid_preserved": True})

    def game_fixture_pid(self):
        return int(self.command(["systemctl", "show", "--property=MainPID", "--value", "minecraft@installer-fixture.service"], expected=0).stdout.strip())

    def rollback_v3(self):
        data = fingerprint(Path("/var/lib/minecraft-ui/qualification-data.marker"))
        world = fingerprint(Path("/var/lib/minecraft-ui/instances/installer-fixture/server/world/qualification-world.marker"))
        before = manager_identity()
        self.prepare_install("v0.0.3", expected_outcome="rolled_back", expected_exit=1)
        after = manager_identity()
        if (before["pid"], before["starttime"]) == (after["pid"], after["starttime"]) or after["version"] != "v0.0.2" or fingerprint(Path("/var/lib/minecraft-ui/qualification-data.marker")) != data or fingerprint(Path("/var/lib/minecraft-ui/instances/installer-fixture/server/world/qualification-world.marker")) != world:
            raise RuntimeError("unhealthy v3 did not restore healthy v2 without changing data")
        self.health()
        self.check("installed", True)
        self.record("unhealthy-v3-upgrade-rollback", {"restored_version": "v0.0.2", "data_preserved": True, "fixture_modified": True})

    def interrupted_recovery(self):
        command = [self.bootstrap, "prepare", "--repository", REPO,
                   "--trust-file", self.candidates / "v0.0.3" / "development-trust.json",
                   "--version", "v0.0.3", "--artifacts", self.candidates / "v0.0.3",
                   "--output", self.scratch / "prepared-interrupted-v3", "--allow-development", "--install"]
        process = subprocess.Popen([str(x) for x in command], stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        transaction = None
        deadline = time.monotonic() + 180
        while time.monotonic() < deadline:
            transaction = read_transaction()
            if transaction and transaction["phase"] in {"replacing", "verifying"}:
                unit = "minecraft-ui-install-" + transaction["id"] + ".service"
                observed = subprocess.run(["systemctl", "show", "--property=MainPID", "--value", unit], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=10, check=False)
                main_pid = int(observed.stdout.strip() or b"0") if observed.returncode == 0 else 0
                if main_pid > 1 and executor_matches(main_pid, transaction["id"]):
                    self.command(["systemctl", "kill", "--kill-who=all", "--signal=SIGKILL", unit], expected=0, timeout=15)
                    break
            if process.poll() is not None:
                break
            time.sleep(0.15)
        else:
            process.kill()
            process.communicate(timeout=15)
            raise RuntimeError("installer never reached the durable interruption boundary")
        if transaction is None or transaction["phase"] not in {"replacing", "verifying"}:
            process.communicate(timeout=30)
            raise RuntimeError("installer exited before the intended interruption phase")
        try:
            process.communicate(timeout=45)
        except subprocess.TimeoutExpired:
            process.kill()
            process.communicate(timeout=15)
            raise RuntimeError("installer caller did not return after its exact worker was killed")
        if process.returncode == 0:
            raise RuntimeError("installer caller reported success after its worker was killed")
        self.check("recovery_required", False, True)
        self.command(["systemctl", "reset-failed", "minecraft-ui.service"], expected=0)
        self.command(["systemctl", "stop", "minecraft-ui.service"], expected=0, timeout=45)
        start = self.command(["systemctl", "start", "minecraft-ui.service"], expected=1, timeout=90)
        del start
        if manager_main_pid() != 0:
            raise RuntimeError("normal manager startup bypassed an unfinished installer transaction")
        recovered = self.cli("install", "recover", timeout=14 * 60)
        response = self.json_result(recovered)
        if response.get("outcome") != "rolled_back":
            raise RuntimeError("independent recovery worker did not roll back interrupted v3")
        self.check("installed", True)
        self.health()
        actual = manager_identity()
        if actual["version"] != "v0.0.2":
            raise RuntimeError("recovery did not restore v2 manager image")
        self.record("killed-installer-worker-and-independent-recovery", {"recovery_outcome": "rolled_back", "restored_version": "v0.0.2", "startup_gate_refused": True})

    def uninstall_and_reinstall(self):
        game_pid = self.game_fixture_pid()
        if game_pid <= 1:
            raise RuntimeError("independent game fixture is not active")
        manager_before_refusal = manager_identity()
        result = self.cli("install", "uninstall", expected=1, timeout=14 * 60)
        response = self.json_result(result)
        manager_after_refusal = manager_identity()
        if (response.get("outcome") not in {"manual_repair", "pending"}
                or self.game_fixture_pid() != game_pid
                or manager_before_refusal != manager_after_refusal):
            raise RuntimeError("uninstall did not refuse the running managed game fixture")
        self.check("installed", True)
        self.game_fixture(False)
        self.command(["systemctl", "stop", "minecraft-ui.service"], expected=0, timeout=180)
        data = fingerprint(Path("/var/lib/minecraft-ui/qualification-data.marker"))
        world = fingerprint(Path("/var/lib/minecraft-ui/instances/installer-fixture/server/world/qualification-world.marker"))
        config = fingerprint(Path("/etc/minecraft-ui/environment"))
        trust = fingerprint(Path("/etc/minecraft-ui/bootstrap-trust.json"))
        receipt = fingerprint(Path("/var/lib/minecraft-ui-update/installed.json"))
        database_path = Path("/var/lib/minecraft-ui/manager.db")
        database_identity = ordinary_file_identity(database_path)
        database_state = database_snapshot(database_path)
        self.command(["systemctl", "start", "minecraft-ui.service"], expected=0, timeout=90)
        self.health()
        self.check("installed", True)
        uninstall = self.cli("install", "uninstall", timeout=14 * 60)
        uninstalled = self.json_result(uninstall)
        if uninstalled.get("outcome") != "uninstalled":
            raise RuntimeError("shipping uninstall did not report completion")
        self.check("uninstalled", True)
        if manager_main_pid() != 0 or Path("/usr/local/bin/minecraft-ui").exists():
            raise RuntimeError("uninstall left active manager or deployment executable")
        if fingerprint(Path("/var/lib/minecraft-ui/qualification-data.marker")) != data or fingerprint(Path("/var/lib/minecraft-ui/instances/installer-fixture/server/world/qualification-world.marker")) != world or fingerprint(Path("/etc/minecraft-ui/environment")) != config or fingerprint(Path("/etc/minecraft-ui/bootstrap-trust.json")) != trust:
            raise RuntimeError("uninstall removed retained data, configuration or trust")
        if (fingerprint(Path("/var/lib/minecraft-ui-update/installed.json")) != receipt
                or ordinary_file_identity(database_path) != database_identity
                or database_snapshot(database_path) != database_state):
            raise RuntimeError("uninstall removed installed receipt or manager database")
        again = self.cli("install", "uninstall", timeout=60)
        if self.json_result(again).get("outcome") != "unchanged":
            raise RuntimeError("second uninstall was not idempotent")
        self.prepare_install("v0.0.2")
        if manager_identity()["version"] != "v0.0.2" or fingerprint(Path("/var/lib/minecraft-ui/qualification-data.marker")) != data or fingerprint(Path("/var/lib/minecraft-ui/instances/installer-fixture/server/world/qualification-world.marker")) != world:
            raise RuntimeError("retained-data reinstall did not restore v2 safely")
        self.health()
        self.check("installed", True)
        self.record("uninstall-refusal-retention-idempotence-reinstall", {"retained_markers": True, "reinstalled_version": "v0.0.2"})

    def updater_independent_fixture(self):
        unit = Path("/etc/systemd/system/minecraft-ui-qualification-independent.service")
        unit.write_text(
            "[Unit]\nDescription=Task30 independent dummy service (not Minecraft)\n"
            "[Service]\nType=simple\nUser=minecraft-ui\nGroup=minecraft-ui\n"
            "ExecStart=/usr/bin/sleep infinity\nNoNewPrivileges=true\nProtectSystem=strict\n"
            "ProtectHome=true\nPrivateTmp=true\nProtectControlGroups=true\n",
            encoding="utf-8")
        os.chown(unit, 0, 0)
        os.chmod(unit, 0o644)
        self.command(["systemctl", "daemon-reload"], expected=0)
        self.command(["systemctl", "start", "minecraft-ui-qualification-independent.service"], expected=0, timeout=60)
        pid = int(self.command(["systemctl", "show", "--property=MainPID", "--value",
                                "minecraft-ui-qualification-independent.service"], expected=0).stdout.strip())
        if pid <= 1:
            raise RuntimeError("independent updater service fixture has no process")
        return pid

    def prepare_updater_fixture(self):
        if self.qualifier is None or self.linux_inputs is None or self.windows_inputs is None:
            raise RuntimeError("updater coexistence native fixture arguments are missing")
        bundle = self.scratch / "updater-fixtures"
        self.command([
            self.qualifier, "prepare",
            "--success", self.linux_inputs / "v0.0.2" / "manager",
            "--negative", self.linux_inputs / "v0.0.3" / "manager",
            "--peer-success", self.windows_inputs / "v0.0.2" / "manager.exe",
            "--peer-negative", self.windows_inputs / "v0.0.3" / "manager.exe",
            "--license", self.repo / "LICENSE",
            "--notices", self.repo / "THIRD_PARTY_NOTICES.txt",
            "--out", bundle,
        ], expected=0, timeout=120)
        trust_source = bundle / "public-trust.json"
        trust_path = Path("/etc/minecraft-ui/update-trust.json")
        if trust_path.exists() or trust_path.is_symlink():
            raise RuntimeError("fresh installer unexpectedly created operator updater trust")
        shutil.copyfile(trust_source, trust_path)
        os.chown(trust_path, 0, 0)
        os.chmod(trust_path, 0o644)
        trust_fingerprint = fingerprint(trust_path)
        if trust_fingerprint["uid"] != 0 or trust_fingerprint["gid"] != 0 or trust_fingerprint["mode"] != 0o644:
            raise RuntimeError("operator updater trust did not receive its fixed root ownership and mode")

        input_dir = Path("/var/lib/minecraft-ui/selfupdate/qualification-input")
        if input_dir.exists() or input_dir.is_symlink():
            raise RuntimeError("updater fixture input destination already exists")
        shutil.copytree(bundle, input_dir)
        uid, gid = account_ids()
        for root, dirs, files in os.walk(input_dir, followlinks=False):
            root_path = Path(root)
            if root_path.is_symlink():
                raise RuntimeError("updater fixture directory contains a symlink")
            os.chown(root_path, uid, gid)
            os.chmod(root_path, 0o700)
            for name in dirs + files:
                child = root_path / name
                if child.is_symlink() or not child.is_file() and not child.is_dir():
                    raise RuntimeError("updater fixture contains an unsafe member")
                os.chown(child, uid, gid)
                os.chmod(child, 0o700 if child.is_dir() else 0o600)

        # Prove the service account can reach the downloaded helper separately
        # from staging, so bounded source-line diagnostics distinguish workspace
        # traversal failures from authenticated staging failures.
        self.command(["runuser", "-u", "minecraft-ui", "--", "test", "-x", self.qualifier], expected=0)
        stages = {}
        for version in ("v0.0.2", "v0.0.3"):
            staged = self.command([
                "runuser", "-u", "minecraft-ui", "--", self.qualifier, "stage",
                "--bundle", input_dir, "--version", version, "--current", "v0.0.1",
                "--stage-dir", "/var/lib/minecraft-ui/selfupdate/stages",
            ], expected=0, timeout=180)
            body = staged.stdout.decode("utf-8", "strict")
            receipt = strict_json(body)
            if not isinstance(receipt, dict) or receipt.get("version") != version or not HEX64.fullmatch(receipt.get("id", "")):
                raise RuntimeError("native updater stager returned an invalid fixture receipt")
            stage_path = self.scratch / (version + "-stage.json")
            stage_path.write_text(body, encoding="utf-8")
            stages[version] = stage_path
        return stages, trust_fingerprint

    def publish_updater_evidence(self, source):
        names = {
            "acceptance-summary.json": "updater-acceptance-summary.json",
            "activation-state-summary.json": "updater-activation-state-summary.json",
            "initial-update-status.json": "updater-initial-update-status.json",
            "interruption-boundary.json": "updater-interruption-boundary.json",
            "api-assertions.txt": "updater-api-assertions.txt",
        }
        for original, published in names.items():
            path = source / original
            if not path.exists() and not path.is_symlink():
                continue
            info = path.lstat()
            if not stat.S_ISREG(info.st_mode) or path.is_symlink() or info.st_nlink != 1 or info.st_size > 65536:
                raise RuntimeError("updater helper evidence file is unsafe or oversized")
            target = self.output / published
            if target.exists() or target.is_symlink():
                raise RuntimeError("updater helper evidence target already exists")
            os.replace(path, target)
            os.chmod(target, 0o644)

    def updater_coexistence(self):
        self.prepare_install("v0.0.1")
        self.check("installed", True)
        self.health()
        trust_path = Path("/etc/minecraft-ui/update-trust.json")
        if trust_path.exists() or trust_path.is_symlink():
            raise RuntimeError("installer created an updater trust configuration by default")
        updater_data = Path("/var/lib/minecraft-ui/qualification-updater.marker")
        updater_data.write_bytes(b"installer and ordinary updater coexistence marker\n")
        uid, gid = account_ids()
        os.chown(updater_data, uid, gid)
        os.chmod(updater_data, 0o600)
        data_fingerprint = fingerprint(updater_data)
        stages, trust_fingerprint = self.prepare_updater_fixture()
        # The native trust source is loaded during manager startup; adding the
        # explicit operator file therefore requires one clean service restart.
        self.command(["systemctl", "stop", "minecraft-ui.service"], expected=0, timeout=180)
        self.command(["systemctl", "start", "minecraft-ui.service"], expected=0, timeout=90)
        self.health()
        self.check("installed", True)
        independent_pid = self.updater_independent_fixture()

        evidence = self.scratch / "updater-evidence"
        evidence.mkdir(mode=0o700)
        api = self.repo / "deploy/qualification/updater_api.py"
        result = None
        try:
            result = self.command([
                "python3", api,
                "--success-stage", stages["v0.0.2"],
                "--negative-stage", stages["v0.0.3"],
                "--initial-binary", self.linux_inputs / "v0.0.1" / "manager",
                "--success-binary", self.linux_inputs / "v0.0.2" / "manager",
                "--negative-binary", self.linux_inputs / "v0.0.3" / "manager",
                "--output", evidence,
            ], expected=None, timeout=20 * 60)
            if len(result.stdout) > 16384:
                raise RuntimeError("updater API fixture output exceeded its bound")
            (evidence / "api-assertions.txt").write_bytes(result.stdout)
            if result.returncode != 0:
                raise RuntimeError("ordinary updater coexistence fixture failed")
        finally:
            self.publish_updater_evidence(evidence)

        independent_after_updater = self.updater_independent_pid()
        if independent_after_updater != independent_pid:
            raise RuntimeError("ordinary updater changed independent fixture unit lifetime")
        api_data = Path("/var/lib/minecraft-ui/qualification-updater-sentinel.bin")
        api_data_fingerprint = fingerprint(api_data)

        if manager_main_pid() != 0:
            raise RuntimeError("ordinary updater fixture left a manager process after its final stop")
        self.command(["systemctl", "start", "minecraft-ui.service"], expected=0, timeout=90)
        self.health()
        check_after_updater = self.check("installed", True)
        expected_hash = hashlib.sha256((self.linux_inputs / "v0.0.2" / "manager").read_bytes()).hexdigest()
        check_image = check_after_updater.get("image")
        if not isinstance(check_image, dict) or check_image.get("version") != "v0.0.2" or check_image.get("sha256") != expected_hash:
            raise RuntimeError("installer check did not reconcile the ordinary updater receipt at v0.0.2")
        self.record("ordinary-updater-terminal-reconciliation", {"version": "v0.0.2", "installer_check_coherent": True})

        applied = self.prepare_install("v0.0.2", expected_outcome="installed")
        if applied.get("failed_stage"):
            raise RuntimeError("installer reapplication after ordinary updater reported a failed stage")
        self.health()
        check_after_apply = self.check("installed", True)
        image = check_after_apply.get("image")
        if not isinstance(image, dict) or image.get("version") != "v0.0.2" or image.get("sha256") != expected_hash:
            raise RuntimeError("shipping installer did not converge updater-reconciled v2 bytes")
        if (fingerprint(updater_data) != data_fingerprint
                or fingerprint(api_data) != api_data_fingerprint
                or fingerprint(trust_path) != trust_fingerprint
                or self.updater_independent_pid() != independent_pid):
            raise RuntimeError("installer reapplication changed updater data, trust, or independent unit")

        self.command(["systemctl", "stop", "minecraft-ui.service"], expected=0, timeout=180)
        database_path = Path("/var/lib/minecraft-ui/manager.db")
        database_identity = ordinary_file_identity(database_path)
        database_state = database_snapshot(database_path)
        self.command(["systemctl", "start", "minecraft-ui.service"], expected=0, timeout=90)
        self.health()
        self.check("installed", True)
        receipt_path = Path("/var/lib/minecraft-ui-update/installed.json")
        installed_receipt = fingerprint(receipt_path)
        uninstalled = self.json_result(self.cli("install", "uninstall", timeout=14 * 60))
        if uninstalled.get("outcome") != "uninstalled":
            raise RuntimeError("installer did not uninstall after updater coexistence")
        self.check("uninstalled", True)
        if (manager_main_pid() != 0 or Path("/usr/local/bin/minecraft-ui").exists()
                or not account_exists()
                or fingerprint(updater_data) != data_fingerprint
                or fingerprint(api_data) != api_data_fingerprint
                or fingerprint(trust_path) != trust_fingerprint
                or fingerprint(receipt_path) != installed_receipt
                or ordinary_file_identity(database_path) != database_identity
                or database_snapshot(database_path) != database_state
                or self.updater_independent_pid() != independent_pid):
            raise RuntimeError("shipping uninstall failed to retain updater trust, manager data, receipt, or independent service")
        self.record("installer-apply-after-updater-and-retaining-uninstall", {
            "version": "v0.0.2", "data_preserved": True, "operator_trust_preserved": True,
            "manager_database_preserved": True, "independent_unit_preserved": True,
        })

    def updater_independent_pid(self):
        return int(self.command(["systemctl", "show", "--property=MainPID", "--value",
                                "minecraft-ui-qualification-independent.service"], expected=0).stdout.strip())

    def retained_failed_install_retry(self):
        self.check("absent", False)
        account_initial = account_exists()
        self.prepare_install("v0.0.3", expected_outcome="rolled_back", expected_exit=1)
        report = self.check("retained", False, False)
        if report.get("image", {}).get("version") != "v0.0.3" or not account_exists() or manager_main_pid() != 0:
            raise RuntimeError("failed first install did not preserve a retryable retained state")
        marker = Path("/var/lib/minecraft-ui/qualification-retained.marker")
        marker.write_bytes(b"retained first-install data\n")
        uid, gid = account_ids()
        os.chown(marker, uid, gid)
        os.chmod(marker, 0o600)
        retained = fingerprint(marker)
        self.prepare_install("v0.0.1")
        self.check("installed", True)
        self.health()
        if fingerprint(marker) != retained or not account_exists() or account_initial:
            raise RuntimeError("retry did not preserve the retained account/data")
        self.record("retained-failed-first-install-retry", {"retry_version": "v0.0.1", "retained_data_preserved": True})

    def health(self):
        url = "http://127.0.0.1:8080/healthz"
        request = urllib.request.Request(url, method="GET")

        class NoRedirect(urllib.request.HTTPRedirectHandler):
            def redirect_request(self, req, fp, code, msg, headers, newurl):
                return None

        opener = urllib.request.build_opener(NoRedirect)
        deadline = time.monotonic() + 30
        last_error = None
        while time.monotonic() < deadline:
            try:
                with opener.open(request, timeout=3) as response:
                    body = response.read(4097)
                    if response.geturl() != url or response.status != 200 or body != b'{"status":"ok"}\n':
                        raise RuntimeError("manager health endpoint response differs")
                    return
            except urllib.error.HTTPError as error:
                if error.code != 503:
                    raise RuntimeError("manager health endpoint response differs") from None
                last_error = error
            except (urllib.error.URLError, TimeoutError) as error:
                last_error = error
            time.sleep(0.2)
        raise RuntimeError("manager health endpoint did not become ready") from last_error

    def execute(self):
        if self.scratch.exists():
            raise RuntimeError("qualification scratch path already exists")
        self.scratch.mkdir(mode=0o700)
        try:
            self.absent_preflight_and_auth_failure()
            if self.scenario == "lifecycle":
                self.fresh_install_and_unchanged()
                self.upgrade_while_game_active()
                self.rollback_v3()
                self.interrupted_recovery()
                self.uninstall_and_reinstall()
            elif self.scenario == "updater-coexistence":
                self.updater_coexistence()
            else:
                self.retained_failed_install_retry()
            self.write_summary("pass")
        finally:
            shutil.rmtree(self.scratch, ignore_errors=True)

    def write_summary(self, status):
        summary = {"format": 1, "scope": "native-ubuntu-linux-installer-lifecycle", "scenario": self.scenario,
                   "status": status, "native_runner": True, "minecraft_game_claim": False,
                   "ordinary_updater_coexistence": "exercised" if self.scenario == "updater-coexistence" else "not exercised in this scenario",
                   "checks": self.checks, "last_command_result": self.last_command_result}
        path = self.output / "acceptance-summary.json"
        path.write_text(json.dumps(summary, sort_keys=True, separators=(",", ":")) + "\n", encoding="utf-8")


def lstat_identity(path):
    try:
        stat = path.lstat()
    except FileNotFoundError:
        return None
    return (stat.st_dev, stat.st_ino, stat.st_uid, stat.st_gid, stat.st_mode, stat.st_size)


def fingerprint(path):
    stat = path.lstat()
    if not path.is_file() or path.is_symlink() or stat.st_nlink != 1:
        raise RuntimeError("expected one ordinary fixture file")
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    return {"device": stat.st_dev, "inode": stat.st_ino, "uid": stat.st_uid,
            "gid": stat.st_gid, "mode": stat.st_mode & 0o777, "size": stat.st_size,
            "sha256": digest}


def ordinary_file_identity(path):
    stat = path.lstat()
    if not path.is_file() or path.is_symlink() or stat.st_nlink != 1:
        raise RuntimeError("expected a single-link ordinary retained file")
    return {"device": stat.st_dev, "inode": stat.st_ino, "uid": stat.st_uid,
            "gid": stat.st_gid, "mode": stat.st_mode & 0o777}


def database_snapshot(path):
    import sqlite3
    connection = sqlite3.connect("file:" + str(path) + "?mode=ro", uri=True, timeout=5)
    try:
        schema = connection.execute(
            "SELECT type,name,sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name"
        ).fetchall()
        tables = {name for kind, name, _ in schema if kind == "table"}
        if "goose_db_version" not in tables or "instances" not in tables or "users" not in tables:
            raise RuntimeError("manager-created database schema is incomplete")
        migrations = connection.execute(
            "SELECT version_id,is_applied FROM goose_db_version ORDER BY id"
        ).fetchall()
        instance_count = connection.execute("SELECT count(*) FROM instances").fetchone()[0]
        user_count = connection.execute("SELECT count(*) FROM users").fetchone()[0]
        schema_bytes = json.dumps(schema, ensure_ascii=True, separators=(",", ":")).encode()
        return {"schema_sha256": hashlib.sha256(schema_bytes).hexdigest(),
                "migration_rows": [[int(version), int(applied)] for version, applied in migrations],
                "instance_count": int(instance_count), "user_count": int(user_count)}
    finally:
        connection.close()


def validate_scenario_args(scenario, qualifier=None, linux_inputs=None, windows_inputs=None):
    optional = (qualifier, linux_inputs, windows_inputs)
    if scenario == "updater-coexistence":
        if any(value is None for value in optional):
            return False
        qualifier, linux_inputs, windows_inputs = map(Path, optional)
        if not (qualifier.is_file() and not qualifier.is_symlink() and os.access(qualifier, os.X_OK)
                and linux_inputs.is_dir() and not linux_inputs.is_symlink()
                and windows_inputs.is_dir() and not windows_inputs.is_symlink()):
            return False
        for version in ("v0.0.1", "v0.0.2", "v0.0.3"):
            linux_manager = linux_inputs / version / "manager"
            windows_manager = windows_inputs / version / "manager.exe"
            for executable in (linux_manager, windows_manager):
                try:
                    info = executable.lstat()
                except OSError:
                    return False
                if (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1
                        or info.st_mode & 0o111 == 0):
                    return False
        return True
    return scenario in {"lifecycle", "retained-retry"} and all(value is None for value in optional)


def create_world_marker(path):
    uid, gid = account_ids()
    base = Path("/var/lib/minecraft-ui/instances")
    ensure_fixture_instances_root(base, uid, gid)
    for directory in [base / "installer-fixture", base / "installer-fixture" / "server",
                      base / "installer-fixture" / "server" / "world"]:
        directory.mkdir(mode=0o700)
        os.chown(directory, uid, gid)
        os.chmod(directory, 0o700)
    with path.open("xb") as marker:
        marker.write(b"qualification world persistence marker; not a Minecraft world\n")
    os.chown(path, uid, gid)
    os.chmod(path, 0o600)


def ensure_fixture_instances_root(base, uid, gid):
    created = False
    try:
        base.mkdir(mode=0o700)
        created = True
    except FileExistsError:
        pass
    info = base.lstat()
    if not stat.S_ISDIR(info.st_mode) or stat.S_ISLNK(info.st_mode):
        raise RuntimeError("manager instances directory is unavailable or unsafe")
    if created:
        os.chown(base, uid, gid)
        os.chmod(base, 0o700)
        info = base.lstat()
    if (info.st_uid != uid or info.st_gid != gid or stat.S_IMODE(info.st_mode) != 0o700):
        raise RuntimeError("manager instances directory ownership or mode differs")


def account_exists():
    return subprocess.run(["getent", "passwd", "minecraft-ui"], stdout=subprocess.DEVNULL,
                          stderr=subprocess.DEVNULL, timeout=5).returncode == 0


def account_ids():
    fields = subprocess.run(["getent", "passwd", "minecraft-ui"], check=True,
                            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=5).stdout.decode().strip().split(":")
    return int(fields[2]), int(fields[3])


def update_trust_disabled():
    path = Path("/etc/minecraft-ui/update-trust.json")
    if not path.exists():
        return True
    trust = strict_json(path.read_bytes())
    if not isinstance(trust, dict) or trust.get("format") != 1 or trust.get("public_keys") != {}:
        raise RuntimeError("ordinary updater trust is not absent or disabled")
    return True


def manager_main_pid():
    value = subprocess.run(["systemctl", "show", "--property=MainPID", "--value", "minecraft-ui.service"],
                           check=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=10).stdout.strip()
    return int(value or b"0")


def manager_identity():
    pid = manager_main_pid()
    if pid <= 1:
        raise RuntimeError("manager service has no main process")
    fields = Path(f"/proc/{pid}/stat").read_text().split()
    starttime = int(fields[21])
    exe = Path(f"/proc/{pid}/exe")
    if not os.path.samefile(exe, "/usr/local/bin/minecraft-ui"):
        raise RuntimeError("manager process is not executing its canonical image")
    return {"pid": pid, "starttime": starttime, "version": manager_version(), "image_sha256": hashlib.sha256(Path("/usr/local/bin/minecraft-ui").read_bytes()).hexdigest()}


def manager_version():
    result = subprocess.run(["/usr/local/bin/minecraft-ui", "version"], check=True,
                            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=10)
    text = result.stdout.decode().strip()
    match = re.fullmatch(r"minecraft-ui (v0\.0\.[123])", text)
    if not match:
        raise RuntimeError("canonical manager version probe mismatch")
    return match.group(1)


def read_transaction():
    return read_transaction_at(Path("/var/lib/minecraft-ui-update/installation-transaction.json"))


def read_transaction_at(path, attempts=5, expected_owner=0, observe=None, wait=time.sleep):
    observe = observe or observe_transaction
    for attempt in range(attempts):
        try:
            before, raw, after = observe(path)
        except FileNotFoundError:
            return None
        identity_before = (before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns)
        identity_after = (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns)
        if identity_before != identity_after:
            if attempt + 1 < attempts:
                wait(0.03)
                continue
            return None
        if (not stat.S_ISREG(before.st_mode) or before.st_uid != expected_owner
                or before.st_mode & 0o777 != 0o600 or before.st_nlink != 1
                or before.st_size < 1 or before.st_size > 16384 or len(raw) != before.st_size):
            raise RuntimeError("installer journal observation is unsafe")
        value = strict_json(raw)
        operation_id = value.get("id") if isinstance(value, dict) else None
        phase = value.get("phase") if isinstance(value, dict) else None
        if not isinstance(operation_id, str) or not HEX64.fullmatch(operation_id) or phase not in PHASES:
            raise RuntimeError("installer journal phase observation is invalid")
        return {"id": operation_id, "phase": phase}
    return None


def observe_transaction(path):
    before = path.lstat()
    raw = path.read_bytes()
    after = path.lstat()
    return before, raw, after


def executor_matches(pid, operation_id):
    expected = Path("/var/lib/minecraft-ui-update/install") / operation_id / "executor"
    return executor_matches_path(expected, Path(f"/proc/{pid}/exe"))


def executor_matches_path(expected, process_exe, expected_uid=0):
    try:
        info = expected.lstat()
        return (stat.S_ISREG(info.st_mode) and info.st_uid == expected_uid
                and info.st_mode & 0o777 == 0o755 and os.path.samefile(process_exe, expected))
    except (FileNotFoundError, OSError):
        return False


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("bootstrap")
    parser.add_argument("candidates")
    parser.add_argument("output")
    parser.add_argument("scenario", choices=("lifecycle", "retained-retry", "updater-coexistence"))
    parser.add_argument("--qualifier")
    parser.add_argument("--linux-inputs")
    parser.add_argument("--windows-inputs")
    args = parser.parse_args()
    if os.environ.get("GITHUB_ACTIONS") != "true" or os.environ.get("RUNNER_ENVIRONMENT") != "github-hosted" or os.geteuid() != 0:
        parser.error("fresh GitHub-hosted Ubuntu runner with root authority required")
    output = Path(args.output)
    if output.is_symlink() or not output.is_dir():
        parser.error("existing ordinary report directory required")
    if not validate_scenario_args(args.scenario, args.qualifier, args.linux_inputs, args.windows_inputs):
        parser.error("updater-coexistence requires executable qualifier and ordinary Linux/Windows native input directories; other scenarios accept none")
    qualification = Qualification(args.bootstrap, args.candidates, output, args.scenario,
                                  args.qualifier, args.linux_inputs, args.windows_inputs)
    try:
        qualification.execute()
    except BaseException as error:
        summary = {"format": 1, "scope": "native-ubuntu-linux-installer-lifecycle", "scenario": args.scenario,
                   "status": "fail", "failure_class": type(error).__name__, "checks": qualification.checks,
                   "last_command_result": qualification.last_command_result,
                   "failure_source_line": failure_source_line(error),
                   "failure_source_lines": failure_source_lines(error)}
        (output / "acceptance-summary.json").write_text(json.dumps(summary, sort_keys=True, separators=(",", ":")) + "\n", encoding="utf-8")
        print("FAIL Linux installer qualification; see bounded summary", file=sys.stderr)
        return 1
    print("PASS native Linux installer lifecycle fixture; no Minecraft support claim")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

#!/usr/bin/env python3
"""Disposable installed updater acceptance. Never emits response/state bodies.

Acquisition was injected through the shipping Stager. Activation requests, drain,
native probes, readiness, rollback and interruption recovery use shipping code.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import time

from manager_api import client, control, ready, request, require


def apply_update(session, stage_id):
    # The shipping cookie-write CSRF gate requires this exact session's token.
    # Keep it in memory and scoped to the mutation; never put it in reports.
    token = request(session, "/api/v1/auth/me").get("csrf_token")
    require(isinstance(token, str) and 0 < len(token) <= 256, "session CSRF token unavailable")
    previous = session.addheaders
    session.addheaders = [(name, value) for name, value in previous if name.lower() != "x-csrf-token"]
    session.addheaders.append(("X-CSRF-Token", token))
    try:
        return request(session, "/api/v1/system/update/apply", {"stage_id": stage_id, "confirm": True}, 202)
    finally:
        session.addheaders = previous


def native(command, timeout=180):
    result = subprocess.run(command, capture_output=True, timeout=timeout)
    require(result.returncode == 0, "native updater acceptance operation failed")
    return result.stdout


def ps(script, timeout=180):
    return native(["pwsh", "-NoProfile", "-Command", "$ErrorActionPreference='Stop'; " + script], timeout)


def digest(path):
    # Ubuntu22.04's system Python is 3.10 (file_digest needs 3.11).
    result = hashlib.sha256()
    with open(path, "rb") as stream:
        for chunk in iter(lambda: stream.read(1 << 20), b""):
            result.update(chunk)
    return result.hexdigest()


def snapshot(expected_hash, expected_sid):
    """Compare real service PID, process identity, actual image and exact bytes."""
    if os.name == "nt":
        value = json.loads(ps(r"""
$s=Get-CimInstance Win32_Service -Filter "Name='MinecraftUI'"
$all=@(Get-CimInstance Win32_Process -Filter "Name='minecraft-ui.exe'" | Where-Object { $_.ExecutablePath -eq 'C:\Program Files\MinecraftUI\minecraft-ui.exe' })
if($all.Count -ne 1 -or $s.State -ne 'Running' -or $s.ProcessId -ne $all[0].ProcessId){throw 'binding'}
$p=$all[0];$o=Invoke-CimMethod -InputObject $p -MethodName GetOwnerSid
if($o.ReturnValue -ne 0){throw 'owner'}
@{pid=$p.ProcessId;owner_sid=$o.Sid;hash=(Get-FileHash -Algorithm SHA256 -LiteralPath $p.ExecutablePath).Hash.ToLowerInvariant();binding=$true}|ConvertTo-Json -Compress
""", 20))
        require(value["owner_sid"] == expected_sid, "installed manager process SID differs")
    else:
        import pwd
        pid = int(native(["systemctl", "show", "--property=MainPID", "--value", "minecraft-ui.service"], 15))
        require(pid > 1, "installed manager service PID absent")
        actual = Path(f"/proc/{pid}/exe")
        require(os.path.samefile(actual, "/usr/local/bin/minecraft-ui"), "service image inode differs")
        expected_uid = pwd.getpwnam("minecraft-ui").pw_uid
        status = Path(f"/proc/{pid}/status").read_text()
        uid = next(line for line in status.splitlines() if line.startswith("Uid:")).split()[1:]
        require(expected_uid != 0 and all(int(item) == expected_uid for item in uid), "manager process UID differs")
        processes = installed_pids()
        require(processes == {pid}, "orphan or duplicate installed manager process")
        value = {"pid": pid, "uid": expected_uid, "hash": digest(actual), "binding": True}
    require(value["hash"] == expected_hash, "installed service executable hash differs")
    return value


def installed_pids():
    if os.name == "nt":
        return {int(p) for p in ps(r"Get-CimInstance Win32_Process -Filter "
                                   r""""Name='minecraft-ui.exe'" | Where-Object { $_.ExecutablePath -eq 'C:\Program Files\MinecraftUI\minecraft-ui.exe' } | ForEach-Object { $_.ProcessId }""", 15).split()}
    found = set()
    for entry in Path("/proc").iterdir():
        if entry.name.isdecimal():
            try:
                image = os.readlink(entry / "exe").removesuffix(" (deleted)")
                if image == "/usr/local/bin/minecraft-ui":
                    found.add(int(entry.name))
            except OSError:
                pass
    return found


def read_journal(state):
    try:
        with open(state / "activation.json", encoding="utf-8") as stream:
            return json.load(stream)
    except FileNotFoundError:
        return None


def safe_initial_status(value):
    """Retain only bounded fixture identity, primitive flags and fixed reasons."""
    if not isinstance(value, dict):
        value = {}
    reasons = {
        "": "none",
        "Release source and verification keys have not been configured.": "trust_unconfigured",
        "Development builds require a reviewed native installation before manager activation.": "development_install_required",
        "The native update and recovery service is not installed or ready.": "native_not_ready",
        "Manager activation requires the canonical native data directory and 127.0.0.1:8080; custom deployments require manual maintenance.": "noncanonical_install",
    }
    reason = value.get("disabled_reason")
    return {
        "current_version": safe_fixture_version(value.get("current_version")),
        "schema": safe_schema(value.get("schema")),
        "trust_configured": safe_boolean(value.get("trust_configured")),
        "activation_available": safe_boolean(value.get("activation_available")),
        "disabled_reason": reasons.get(reason, "unrecognized") if isinstance(reason, str) else "unrecognized",
    }


def safe_fixture_version(value):
    return value if isinstance(value, str) and value in {"v0.0.1", "v0.0.2", "v0.0.3"} else "unexpected"


def safe_schema(value):
    return value if type(value) is int and 0 < value <= 2147483647 else None


def safe_boolean(value):
    return value if type(value) is bool else None


def safe_hash(value):
    return value if isinstance(value, str) and len(value) == 64 and all(c in "0123456789abcdef" for c in value) else None


def write_journal_evidence(state, output):
    """Read at most 16 KiB; never retain nonce, actor, timestamps or error text."""
    evidence = {"journal": "unreadable"}
    try:
        with open(state / "activation.json", "rb") as stream:
            body = stream.read(16385)
        if len(body) > 16384:
            evidence = {"journal": "oversized"}
        else:
            journal = json.loads(body)
            if not isinstance(journal, dict):
                raise ValueError("not an object")
            phase = journal.get("phase")
            phases = {"prepared", "stopped", "replacing", "candidate", "healthy", "rollback", "rolled_back", "aborted"}
            request = journal.get("request")
            evidence = {
                "journal": "present",
                "phase": phase if isinstance(phase, str) and phase in phases else "unrecognized",
                "stage_id": safe_hash(request.get("stage_id")) if isinstance(request, dict) else None,
                "rollback_attempted": safe_boolean(journal.get("rollback_attempted")),
            }
            for name in ("prior", "candidate"):
                installed = journal.get(name)
                installed = installed if isinstance(installed, dict) else {}
                evidence[name] = {"version": safe_fixture_version(installed.get("version")),
                                  "schema": safe_schema(installed.get("schema")),
                                  "sha256": safe_hash(installed.get("sha256"))}
    except FileNotFoundError:
        evidence = {"journal": "absent"}
    except (ValueError, UnicodeError, RecursionError):
        evidence = {"journal": "malformed"}
    except OSError:
        pass
    try:
        (output / "activation-state-summary.json").write_text(json.dumps(evidence, indent=2))
    except OSError:
        raise RuntimeError("native updater diagnostic could not be retained") from None


def wait_for(predicate, seconds, message):
    until = time.monotonic() + seconds
    while time.monotonic() < until:
        try:
            value = predicate()
            if value:
                return value
        except (OSError, ValueError, RuntimeError, subprocess.SubprocessError):
            pass
        time.sleep(0.15)
    raise RuntimeError(message)


def wait_terminal(state, stage, phase, output=None):
    def terminal():
        journal = read_journal(state)
        if journal and journal["request"]["stage_id"] == stage and journal["phase"] in {"healthy", "rolled_back", "aborted"}:
            return journal
        return None
    try:
        journal = wait_for(terminal, 310, "native updater terminal state timed out")
        # Definitive terminal rejection must escape the transient read retry.
        require(journal["phase"] == phase, "unexpected native updater terminal state")
        return journal
    finally:
        if output is not None:
            write_journal_evidence(state, output)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--success-stage", required=True)
    parser.add_argument("--negative-stage", required=True)
    parser.add_argument("--initial-binary", required=True)
    parser.add_argument("--success-binary", required=True)
    parser.add_argument("--negative-binary", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--expected-sid", default="")
    args = parser.parse_args()
    require(os.environ.get("GITHUB_ACTIONS") == "true" and os.environ.get("RUNNER_ENVIRONMENT") == "github-hosted", "disposable hosted runner required")
    output = Path(args.output)
    require(output.is_dir() and not output.is_symlink(), "report directory absent or unsafe")
    data = Path(r"C:\ProgramData\MinecraftUI" if os.name == "nt" else "/var/lib/minecraft-ui")
    state = Path(r"C:\ProgramData\MinecraftUIUpdate" if os.name == "nt" else "/var/lib/minecraft-ui-update")
    binary = Path(r"C:\Program Files\MinecraftUI\minecraft-ui.exe" if os.name == "nt" else "/usr/local/bin/minecraft-ui")
    success = json.loads(Path(args.success_stage).read_text())
    negative = json.loads(Path(args.negative_stage).read_text())
    require(success["version"] == "v0.0.2" and negative["version"] == "v0.0.3", "fixture stage version differs")
    require(success["schema"] == negative["schema"], "fixture schema differs")
    schema = success["schema"]
    initial_hash, success_hash, negative_hash = map(digest, (args.initial_binary, args.success_binary, args.negative_binary))
    require(success["binary_sha256"] == success_hash and negative["binary_sha256"] == negative_hash, "fixture stage bytes differ")
    ready()
    require(request(client(), "/api/v1/auth/status")["needs_setup"], "canonical database was not empty")
    credentials = {"username": "updater-acceptance-admin", "password": secrets.token_urlsafe(36)}
    session = client()
    user = request(session, "/api/v1/auth/setup", credentials, 201)["user"]
    require(user["role"] == "admin", "real setup did not create administrator")
    sentinel = data / "qualification-updater-sentinel.bin"
    require(not sentinel.exists(), "sentinel already exists")
    sentinel.write_bytes(secrets.token_bytes(4096))
    sentinel_hash = digest(sentinel)
    db_identity = (data / "manager.db").stat()
    first = snapshot(initial_hash, args.expected_sid)
    dummy_pid = None
    if os.name != "nt":
        dummy_pid = int(native(["systemctl", "show", "--property=MainPID", "--value", "minecraft-ui-qualification-independent.service"], 15))
        require(dummy_pid > 1, "independent dummy service absent")
    initial_status = request(session, "/api/v1/system/update")
    (output / "initial-update-status.json").write_text(json.dumps(safe_initial_status(initial_status), indent=2))
    require(initial_status["current_version"] == "v0.0.1" and initial_status["schema"] == schema and initial_status["trust_configured"] and initial_status["activation_available"], "initial installed updater unavailable or identity differs")
    summaries = []

    def preserved(label, expected_phase, candidate, previous_pid):
        ready()
        status = request(session, "/api/v1/system/update")
        require(status["current_version"] == "v0.0.2" and status["schema"] == schema, "healthy manager version/schema differs")
        require(request(session, "/api/v1/auth/me")["user"]["id"] == user["id"], "administrator session lost")
        require(request(client(), "/api/v1/auth/login", credentials)["user"]["id"] == user["id"], "administrator login lost")
        require(not request(client(), "/api/v1/auth/status")["needs_setup"], "administrator database lost")
        require(os.path.samestat(db_identity, (data / "manager.db").stat()), "database file replaced")
        require(digest(sentinel) == sentinel_hash, "application data sentinel changed")
        require(digest(binary) == success_hash, "prior genuine manager bytes not restored")
        with open(state / "installed.json", encoding="utf-8") as stream:
            installed = json.load(stream)
        require(installed == {"format": 1, "version": "v0.0.2", "schema": schema, "sha256": success_hash}, "installed receipt differs")
        actual = snapshot(success_hash, args.expected_sid)
        if dummy_pid is not None:
            require(int(native(["systemctl", "show", "--property=MainPID", "--value", "minecraft-ui-qualification-independent.service"], 15)) == dummy_pid,
                    "manager update stopped independent dummy service")
        require(actual["pid"] != previous_pid and previous_pid not in installed_pids(), "old manager process survived activation")
        journal = read_journal(state)
        require(journal["phase"] == expected_phase and journal["candidate"]["sha256"] == candidate, "native terminal journal differs")
        require(journal["rollback_attempted"] == (expected_phase == "rolled_back"), "native rollback attempt state differs")
        require(not any((data / "selfupdate/control" / name).exists() for name in ("request.json", "challenge.json", "drained.json")), "activation control state not reconciled")
        summary = {"case": label, "phase": expected_phase, "current_version": "v0.0.2", "schema": schema, "sha256": success_hash,
                   "candidate_sha256": candidate, "process": actual, "prior_process_pid": previous_pid, "admin_session_data_preserved": True,
                   "rollback_attempted": journal["rollback_attempted"]}
        if dummy_pid is not None:
            summary["independent_dummy_pid_unchanged"] = dummy_pid
        summaries.append(summary)
        (output / "acceptance-summary.json").write_text(json.dumps(summaries, indent=2))
        print("PASS installed updater " + label, flush=True)
        return actual

    apply_update(session, success["id"])
    journal = wait_terminal(state, success["id"], "healthy", output)
    require(not journal["rollback_attempted"], "successful activation attempted rollback")
    after_success = preserved("genuine-success", "healthy", success_hash, first["pid"])

    apply_update(session, negative["id"])
    # Require a real running negative service/image. A probe rejection or abort
    # cannot accidentally qualify the health-failure recovery case.
    def negative_running():
        journal = read_journal(state)
        if journal and journal["request"]["stage_id"] == negative["id"] and journal["phase"] == "candidate":
            return snapshot(negative_hash, args.expected_sid)
        return None
    negative_process = wait_for(negative_running, 80, "negative candidate never reached actual native service")
    wait_terminal(state, negative["id"], "rolled_back", output)
    after_rollback = preserved("negative-health-rollback", "rolled_back", negative_hash, negative_process["pid"])
    require(after_success["pid"] not in installed_pids(), "prior manager process survived negative activation")

    apply_update(session, negative["id"])
    interrupted_process = wait_for(negative_running, 80, "interruption candidate boundary absent")
    # candidate is saved after verified replacement and its durability fence.
    # The unhealthy fixture holds that phase for the fixed 90s readiness gate.
    if os.name == "nt":
        worker = json.loads(ps(r"""$u=Get-CimInstance Win32_Service -Filter "Name='MinecraftUIUpdater'";if($u.State -ne 'Running' -or $u.ProcessId -lt 1){throw 'updater'};$p=Get-CimInstance Win32_Process -Filter ("ProcessId="+$u.ProcessId);if($p.ExecutablePath -ne 'C:\Program Files\MinecraftUI\selfupdate.exe'){throw 'image'};$o=Invoke-CimMethod -InputObject $p -MethodName GetOwnerSid;if($o.ReturnValue -ne 0 -or $o.Sid -ne 'S-1-5-18'){throw 'owner'};$e=@{pid=$u.ProcessId;owner_sid=$o.Sid;binding=$true};Stop-Process -Id $u.ProcessId -Force;(Get-Service MinecraftUIUpdater).WaitForStatus('Stopped',[TimeSpan]::FromSeconds(30));$e|ConvertTo-Json -Compress""", 45))
    else:
        worker_pid = int(native(["systemctl", "show", "--property=MainPID", "--value", "minecraft-ui-update@" + negative["id"] + ".service"], 15))
        require(worker_pid > 1 and os.path.samefile(f"/proc/{worker_pid}/exe", "/usr/local/lib/minecraft-ui/selfupdate"), "interrupted updater worker image differs")
        worker = {"pid": worker_pid, "binding": True}
        native(["systemctl", "kill", "--signal=SIGKILL", "minecraft-ui-update@" + negative["id"] + ".service"], 20)
        wait_for(lambda: native(["systemctl", "show", "--property=MainPID", "--value", "minecraft-ui-update@" + negative["id"] + ".service"], 10).strip() == b"0", 20, "updater worker did not exit")
    frozen = read_journal(state)
    require(frozen["phase"] == "candidate" and frozen["candidate"]["sha256"] == negative_hash and not frozen["rollback_attempted"], "interruption missed durable candidate boundary")
    (output / "interruption-boundary.json").write_text(json.dumps({"phase": "candidate", "candidate_sha256": negative_hash, "candidate_process": interrupted_process,
                                                                 "updater_worker": worker, "rollback_attempted_before_recovery": False}, indent=2))
    control("stop")
    require(not installed_pids(), "negative manager survived explicit native stop")
    if os.name == "nt":
        ps("Start-Service MinecraftUIUpdater; (Get-Service MinecraftUIUpdater).WaitForStatus('Running',[TimeSpan]::FromSeconds(180))", 220)
    # Shipping systemd recovery dependency / SCM updater startup performs the
    # recovery; the harness never edits executable or activation state.
    control("start")
    wait_terminal(state, negative["id"], "rolled_back", output)
    preserved("interrupted-candidate-recovery", "rolled_back", negative_hash, interrupted_process["pid"])
    require(after_rollback["pid"] not in installed_pids(), "old manager survived interrupted activation")
    control("stop")
    require(not installed_pids(), "orphan manager at final native stop")
    print("PASS final native stop: no orphan manager", flush=True)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print("FAIL installed updater acceptance (" + type(error).__name__ + ")", file=sys.stderr)
        if isinstance(error, RuntimeError):
            print(str(error), file=sys.stderr)
        sys.exit(1)

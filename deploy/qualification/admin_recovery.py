"""Installed native password-recovery acceptance; no standalone service setup.

Called only by manager_api.py on fresh hosted-runner state. Secrets remain in
memory. Subprocess output, HTTP bodies and external exception text never escape.
"""
import json
import os
from pathlib import Path
import secrets
import shutil
import sqlite3
import subprocess
import tempfile

SIDECARS = ("manager.db", "manager.db-wal", "manager.db-shm", "manager.db-journal")


def _require(condition):
    if not condition:
        raise RuntimeError("recovery assertion failed")


def _command(password):
    arguments = ["admin-reset", "--username", "native-smoke-admin", "--password-stdin"]
    if os.name == "nt":
        command = [r"C:\Program Files\MinecraftUI\minecraft-ui.exe", *arguments]
        data = r"C:\ProgramData\MinecraftUI"
    else:
        command = ["sudo", "-n", "-u", "minecraft-ui", "env",
                   "MINECRAFT_UI_DATA_DIR=/var/lib/minecraft-ui",
                   "/usr/local/bin/minecraft-ui", *arguments]
        data = "/var/lib/minecraft-ui"
    environment = os.environ.copy()
    environment["MINECRAFT_UI_DATA_DIR"] = data
    result = subprocess.run(command, input=(password + "\n").encode("utf-8"),
                            capture_output=True, timeout=45, env=environment)
    # Both outputs stay captured. Detect any accidental echo without printing it.
    _require(password.encode("utf-8") not in result.stdout + result.stderr)
    return result


def _snapshot(data):
    """Copy under native manager lock; inspect only the disposable private copy."""
    snapshot = Path(tempfile.mkdtemp(prefix="admin-recovery-", dir=data))
    try:
        helper = Path(__file__).resolve().parent
        if os.name == "nt":
            command = ["pwsh", "-NoProfile", "-File",
                       str(helper / "admin_recovery_snapshot_windows.ps1"),
                       "-Snapshot", str(snapshot)]
        else:
            command = ["bash", str(helper / "admin_recovery_snapshot_linux.sh"), str(snapshot)]
        result = subprocess.run(command, capture_output=True, timeout=45)
        _require(result.returncode == 0)
        report = json.loads(result.stdout)
        _require(set(report) == {"snapshot_files", "complete_sidecars", "hash_match",
                                 "lock_contention_refused"})
        _require(report["complete_sidecars"] is True and report["hash_match"] is True
                 and report["lock_contention_refused"] is True)
        _require(type(report["snapshot_files"]) is int and 1 <= report["snapshot_files"] <= 4)
        expected = {name for name in SIDECARS if (data / name).exists()}
        _require({path.name for path in snapshot.iterdir()} == expected)
        _require(len(expected) == report["snapshot_files"])
        _verify_snapshot(snapshot)
        return report["snapshot_files"]
    finally:
        # This directory contains credential-bearing DB/WAL bytes. It never
        # enters an artifact directory; outer native cleanup also removes data.
        shutil.rmtree(snapshot)


def _verify_snapshot(snapshot):
    # Read-only avoids changing the private evidence before checking integrity;
    # SQLite consumes copied WAL when present. Do not use immutable=1 (ignores WAL).
    uri = (snapshot / "manager.db").as_uri() + "?mode=ro"
    database = sqlite3.connect(uri, uri=True)
    try:
        database.execute("PRAGMA query_only=ON")
        _require(database.execute("PRAGMA integrity_check").fetchall() == [("ok",)])
        _require(database.execute("SELECT COUNT(*) FROM users WHERE username=? AND role='admin' AND disabled=0",
                                  ("native-smoke-admin",)).fetchone() == (1,))
    finally:
        database.close()


def qualify_recovery(*, client, request, ready, control, manager_pids, session,
                     identity, credentials):
    """Extend the real setup identity; all HTTP mutations use shipping APIs."""
    stage = "runner-guard"
    try:
        _require(os.environ.get("GITHUB_ACTIONS") == "true"
                 and os.environ.get("RUNNER_ENVIRONMENT") == "github-hosted")
        data = Path(r"C:\ProgramData\MinecraftUI" if os.name == "nt" else "/var/lib/minecraft-ui")
        _require(data.is_dir() and not data.is_symlink() and (data / "manager.db").is_file())
        _require(credentials["username"] == "native-smoke-admin")
        stage = "api-credentials"
        session.addheaders = [("X-CSRF-Token", request(session, "/api/v1/auth/me")["csrf_token"])]
        token = request(session, "/api/v1/auth/tokens",
                        {"name": "native-recovery", "expires_in_days": 1, "scopes": ["read"]}, 201)["secret"]
        bearer = client()
        bearer.addheaders = [("Authorization", "Bearer " + token)]
        request(bearer, "/api/v1/instances")
        other_credentials = {"username": "native-smoke-other", "password": secrets.token_urlsafe(36)}
        other = request(session, "/api/v1/users", {**other_credentials, "role": "viewer"}, 201)["user"]
        other_session = client()
        _require(request(other_session, "/api/v1/auth/login", other_credentials)["user"]["id"] == other["id"])
        before_users = request(session, "/api/v1/users")["users"]
        _require(len(before_users) == 2)
        stage = "running-manager-refusal"
        before = manager_pids()
        _require(len(before) == 1)
        refused = _command(secrets.token_urlsafe(36))
        _require(refused.returncode != 0 and b"manager lock" in refused.stderr.lower())
        _require(manager_pids() == before)
        _require(request(client(), "/api/v1/auth/login", credentials)["user"]["id"] == identity)
        _require(request(session, "/api/v1/auth/me")["user"]["id"] == identity)
        request(bearer, "/api/v1/instances")
        print("PASS recovery running-manager lock refusal; old login/session/token valid", flush=True)
        stage = "native-stop"
        control("stop")
        _require(not manager_pids())
        stage = "stopped-snapshot"
        snapshot_files = _snapshot(data)
        print(f"PASS recovery stopped SQLite snapshot: {snapshot_files} existing files; exact lock/contention/hashes/integrity", flush=True)
        stage = "offline-reset"
        new_password = secrets.token_urlsafe(36)
        reset = _command(new_password)
        _require(reset.returncode == 0 and b"sessions and personal access tokens revoked" in reset.stdout)
        _require(not manager_pids())
        stage = "native-start"
        control("start")
        ready()
        after = manager_pids()
        _require(len(after) == 1 and not before.intersection(after))
        stage = "revocation-and-persistence"
        recovered = client()
        actual = request(recovered, "/api/v1/auth/login",
                         {"username": credentials["username"], "password": new_password})["user"]
        _require(actual["id"] == identity and actual["role"] == "admin")
        _require(request(client(), "/api/v1/auth/status")["needs_setup"] is False)
        request(client(), "/api/v1/auth/login", credentials, 401)
        request(session, "/api/v1/auth/me", expected=401)
        request(bearer, "/api/v1/instances", expected=401)
        _require(request(recovered, "/api/v1/auth/tokens")["tokens"] == [])
        _require(request(other_session, "/api/v1/auth/me")["user"]["id"] == other["id"])
        _require(request(client(), "/api/v1/auth/login", other_credentials)["user"]["id"] == other["id"])
        after_users = request(recovered, "/api/v1/users")["users"]
        _require({(user["id"], user["username"], user["role"]) for user in after_users}
                 == {(user["id"], user["username"], user["role"]) for user in before_users})
        print("PASS recovery installed CLI: same admin/new login; old password/session/token rejected; other user/session preserved", flush=True)
    except Exception:
        # A dependency RuntimeError can contain arbitrary text; replace every
        # exception with the fixed stage, never its body or a traceback.
        raise RuntimeError("admin recovery failed at " + stage) from None

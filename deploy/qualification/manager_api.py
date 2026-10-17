#!/usr/bin/env python3
"""Installed-service onboarding and persistence smoke; secrets stay in memory.

Only run from the disposable-host native harness. Prints assertions, never HTTP
bodies, cookies, passwords, database contents or subprocess output.
"""
import http.cookiejar
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import time
import urllib.error
import urllib.request

BASE = "http://127.0.0.1:8080"


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def request(client, path, payload=None, expected=200):
    data = None if payload is None else json.dumps(payload).encode()
    req = urllib.request.Request(BASE + path, data=data,
                                 headers={"Content-Type": "application/json"})
    try:
        response = client.open(req, timeout=5)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        require(response.status == expected, f"unexpected HTTP status at {path}: {response.status}")
        require(response.headers.get_content_type() == "application/json", f"non-JSON response at {path}")
        return json.loads(response.read(65536))


def client():
    return urllib.request.build_opener(urllib.request.ProxyHandler({}),
                                      urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))


def ready():
    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        try:
            if request(client(), "/healthz") == {"status": "ok"}:
                return
        except (OSError, RuntimeError, ValueError):
            pass
        time.sleep(1)
    raise RuntimeError("installed manager did not become ready in 60 seconds")


def manager_pids():
    if os.name == "nt":
        script = "Get-CimInstance Win32_Process -Filter \"Name='minecraft-ui.exe'\" | Where-Object { $_.ExecutablePath -eq 'C:\\Program Files\\MinecraftUI\\minecraft-ui.exe' } | ForEach-Object { $_.ProcessId }"
        result = subprocess.run(["pwsh", "-NoProfile", "-Command", script],
                                capture_output=True, timeout=15, check=True)
        return {int(value) for value in result.stdout.split()}
    found = set()
    for entry in Path("/proc").iterdir():
        if not entry.name.isdecimal():
            continue
        try:
            if os.readlink(entry / "exe") == "/usr/local/bin/minecraft-ui":
                found.add(int(entry.name))
        except (OSError, PermissionError):
            pass
    return found


def control(action):
    if os.name == "nt":
        # SCM waits are bounded independently of the outer subprocess timeout.
        scripts = {
            "stop": "Stop-Service MinecraftUI; (Get-Service MinecraftUI).WaitForStatus('Stopped',[TimeSpan]::FromSeconds(160))",
            "start": "Start-Service MinecraftUI; (Get-Service MinecraftUI).WaitForStatus('Running',[TimeSpan]::FromSeconds(60))",
            "restart": "Stop-Service MinecraftUI; (Get-Service MinecraftUI).WaitForStatus('Stopped',[TimeSpan]::FromSeconds(160)); Start-Service MinecraftUI; (Get-Service MinecraftUI).WaitForStatus('Running',[TimeSpan]::FromSeconds(60))",
        }
        command = ["pwsh", "-NoProfile", "-Command", "$ErrorActionPreference='Stop'; " + scripts[action]]
    else:
        command = ["systemctl", action, "minecraft-ui.service"]
    result = subprocess.run(command, capture_output=True, timeout=240)
    require(result.returncode == 0, f"native service {action} failed")


def main():
    require(os.environ.get("GITHUB_ACTIONS") == "true", "ephemeral GitHub runner required")
    ready()
    require(request(client(), "/api/v1/auth/status")["needs_setup"] is True,
            "canonical database was not empty")
    password = secrets.token_urlsafe(36)
    credentials = {"username": "native-smoke-admin", "password": password}
    session = client()
    created = request(session, "/api/v1/auth/setup", credentials, 201)["user"]
    require(created["role"] == "admin", "setup did not create administrator")
    identity = created["id"]
    require(request(session, "/api/v1/auth/me")["user"]["id"] == identity,
            "setup session is not authenticated")
    print("PASS empty DB -> setup -> authenticated administrator", flush=True)
    for action in ("stop/start", "restart"):
        before = manager_pids()
        require(len(before) == 1, "expected exactly one installed manager process")
        if action == "stop/start":
            control("stop")
            require(not manager_pids(), "manager process survived SCM/systemd stop")
            try:
                urllib.request.urlopen(BASE + "/healthz", timeout=2)
            except OSError:
                pass
            else:
                raise RuntimeError("HTTP listener survived manager stop")
            control("start")
        else:
            control("restart")
        ready()
        after = manager_pids()
        require(len(after) == 1 and not before.intersection(after),
                "restart left an old or duplicate manager process")
        require(request(client(), "/api/v1/auth/status")["needs_setup"] is False,
                "administrator state did not persist")
        require(request(session, "/api/v1/auth/me")["user"]["id"] == identity,
                "session did not persist")
        login = client()
        require(request(login, "/api/v1/auth/login", credentials)["user"]["id"] == identity,
                "persisted administrator cannot log in")
        print(f"PASS native {action}: persisted admin/session/login; old process gone", flush=True)
    control("stop")
    require(not manager_pids(), "orphan installed manager after final stop")
    print("PASS final native stop: zero installed manager processes", flush=True)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        # Error strings are fixed assertions above; external response/subprocess
        # bodies and tracebacks could contain credentials and are not emitted.
        print(f"FAIL installed API smoke ({type(error).__name__})", file=sys.stderr)
        if isinstance(error, RuntimeError):
            print(str(error), file=sys.stderr)
        sys.exit(1)

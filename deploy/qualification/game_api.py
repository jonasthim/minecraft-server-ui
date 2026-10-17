#!/usr/bin/env python3
"""Hosted-only shipping API + real protocol client acceptance.

No product state is written by this program except through HTTP/gameplay. Native
identity/runtime/lease records are read narrowly as corroborating evidence. Raw
HTTP, logs, archives, subprocess output and credentials never enter reports.
"""
import argparse
import datetime
import gzip
import hashlib
import http.cookiejar
import io
import json
import math
import os
from pathlib import Path, PurePosixPath
import queue
import re
import secrets
import sqlite3
import stat
import struct
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request
import uuid
import zipfile

from manager_api import control, ready

BASE = "http://127.0.0.1:8080"
INSTANCE = "game-acceptance"
PREFIX = "/api/v1/instances/" + INSTANCE
VERSION = "1.21.9"
YAW_A, YAW_B = 31.25, 121.25
DATA = Path(r"C:\ProgramData\MinecraftUI" if os.name == "nt" else "/var/lib/minecraft-ui")
HERE = Path(__file__).resolve().parent
IDENTIFIER = re.compile(r"^[a-zA-Z0-9_-]{1,64}$")
SHA256 = re.compile(r"^[a-f0-9]{64}$")
PERMISSION_PATHS = (
    "/", "/usr", "/usr/local", "/usr/local/bin", "/usr/local/bin/minecraft-ui",
    "/usr/local/lib", "/usr/local/lib/minecraft-ui", "/usr/local/lib/minecraft-ui/unitctl",
    "/etc", "/etc/systemd", "/etc/systemd/system", "/etc/systemd/system/minecraft@.service",
)


class Failure(Exception):
    def __init__(self, code, **evidence):
        super().__init__(code)
        self.code = code
        self.evidence = evidence


def require(condition, code):
    if not condition:
        raise Failure(code)


def hosted():
    require(os.environ.get("GITHUB_ACTIONS") == "true" and
            os.environ.get("RUNNER_ENVIRONMENT") == "github-hosted", "hosted_only")


def permission_snapshot(phase):
    require(phase in {"before-prefix-provisioning", "after-prefix-provisioning", "before-cleanup", "after-prefix-restoration"}, "permission_phase")
    records = []
    for path in PERMISSION_PATHS:
        record = {"path": path}
        try:
            info = os.lstat(path)
            record.update(exists=True, uid=info.st_uid, gid=info.st_gid, mode=format(stat.S_IMODE(info.st_mode), "o"),
                          device=info.st_dev, inode=info.st_ino, directory=stat.S_ISDIR(info.st_mode),
                          regular=stat.S_ISREG(info.st_mode), symlink=stat.S_ISLNK(info.st_mode))
        except FileNotFoundError:
            record["exists"] = False
        except OSError:
            record["metadata_read_failed"] = True
        records.append(record)
    return {"phase": phase, "paths": records}


def safe_directory(value):
    path = Path(value)
    require(path.is_absolute() and path.is_dir(), "output_directory")
    require(all(not p.is_symlink() for p in (path, *path.parents)), "output_symlink")
    for entry in path.iterdir():
        require(entry.name in {"commit.txt", "go-version.txt", "runner.txt"} and
                entry.is_file() and not entry.is_symlink() and entry.stat().st_size <= 4096, "output_not_empty")
    return path


def safe_id(value):
    require(isinstance(value, str) and IDENTIFIER.fullmatch(value), "response_identifier_invalid")
    return value


def api_operation(path, payload):
    method = "GET" if payload is None else "POST"
    fixed = {
        ("GET", "/api/v1/auth/status"): "auth_status", ("POST", "/api/v1/auth/setup"): "auth_setup",
        ("GET", "/api/v1/auth/me"): "auth_me", ("POST", "/api/v1/instances"): "instance_create",
        ("GET", PREFIX): "instance_detail", ("GET", PREFIX + "/lifecycle"): "lifecycle_state",
        ("POST", PREFIX + "/lifecycle/preview"): "lifecycle_preview", ("POST", PREFIX + "/lifecycle"): "lifecycle_mutation",
        ("GET", PREFIX + "/backups"): "backup_list", ("POST", PREFIX + "/backups"): "backup_create",
        ("GET", PREFIX + "/console/status"): "console_status",
    }
    if (method, path) in fixed:
        return fixed[(method, path)]
    if method == "GET" and re.fullmatch(re.escape(PREFIX) + r"/console/logs\?after=[0-9]+&limit=500", path):
        return "console_logs"
    if re.fullmatch(re.escape(PREFIX) + r"/backups/[a-f0-9]{32}/restore", path) and method == "POST":
        return "backup_restore"
    if re.fullmatch(re.escape(PREFIX) + r"/backups/[a-f0-9]{32}/download", path) and method == "GET":
        return "backup_download"
    if re.fullmatch(r"/api/v1/jobs/[a-zA-Z0-9_-]{1,64}", path) and method == "GET":
        return "job_state"
    if re.fullmatch(r"/api/v1/jobs/[a-zA-Z0-9_-]{1,64}/logs\?after=0&limit=500", path) and method == "GET":
        return "job_logs"
    return "unclassified"


def digest(path):
    value = hashlib.sha256()
    with open(path, "rb") as stream:
        for chunk in iter(lambda: stream.read(1 << 20), b""):
            value.update(chunk)
    return value.hexdigest()


def bounded_json(path, limit=1 << 20):
    require(path.is_file() and not path.is_symlink() and path.stat().st_size <= limit,
            "private_evidence_file_invalid")
    return json.loads(path.read_bytes())


class API:
    def __init__(self):
        self.jar = http.cookiejar.CookieJar()
        self.client = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect(),
                         urllib.request.HTTPCookieProcessor(self.jar))
        self.csrf = None

    def open(self, path, payload=None, expected=200, timeout=30):
        require(path.startswith("/api/v1/") and not any(c in path for c in "\r\n\\"), "api_path_invalid")
        headers = {"Content-Type": "application/json", "Origin": BASE}
        if self.csrf:
            headers["X-CSRF-Token"] = self.csrf
        request = urllib.request.Request(BASE + path, headers=headers,
                       data=None if payload is None else json.dumps(payload).encode())
        try:
            response = self.client.open(request, timeout=timeout)
        except urllib.error.HTTPError as error:
            response = error
        if response.status != expected:
            status = response.status
            # Parse only the public enum. Never report server messages/details.
            code = "unclassified"
            try:
                body = json.loads(response.read(65536))
                candidate = body.get("error", {}).get("code")
                if candidate in {"validation_failed", "unsupported", "unavailable", "conflict", "instance_busy", "forbidden", "not_found", "unauthenticated", "port_in_use", "internal"}:
                    code = candidate
            except (ValueError, AttributeError, TypeError):
                pass
            finally:
                response.close()
            raise Failure("api_http_status", http_status=status, api_code=code, api_operation=api_operation(path, payload))
        return response

    def request(self, path, payload=None, expected=200, timeout=30):
        with self.open(path, payload, expected, timeout) as response:
            require(response.headers.get_content_type() == "application/json", "api_not_json")
            raw = response.read((2 << 20) + 1)
            require(len(raw) <= 2 << 20, "api_body_bound")
            return json.loads(raw)

    def job(self, response, timeout=900):
        job = response["job"]
        identifier = safe_id(job["id"])
        until = time.monotonic() + timeout
        while time.monotonic() < until:
            job = self.request("/api/v1/jobs/" + identifier)["job"]
            state = job.get("state")
            if state in {"succeeded", "failed", "cancelled"}:
                phase = job.get("phase", "unknown")
                phase = phase if isinstance(phase, str) and re.fullmatch(r"[a-z_]{1,64}", phase) else "unclassified"
                if state != "succeeded":
                    raise Failure("job_terminal_failure", job_state=state, job_phase=phase,
                                  job_diagnostics=self.job_diagnostics(identifier),
                                  console_diagnostics=self.console_diagnostics())
                return job
            time.sleep(1)
        raise Failure("job_timeout")

    def job_diagnostics(self, identifier):
        # Job.error is intentionally generic. Classify only exact authored
        # public lifecycle messages from one bounded page; never emit its text,
        # wrapped causes, arbitrary paths/URLs, or raw job output.
        messages = {
            "Lifecycle operation failed; inspect its phase and current process state before retrying.": "lifecycle_failure_unspecified",
            "install preview expired or instance changed; resolve again": "install_preview_stale",
            "original selection changed since preview": "install_selection_changed",
            "install preview no longer matches the exact instance selection": "install_selection_mismatch",
            "preview Java selection changed": "install_java_selection_changed",
            "managed distribution/runtime service unavailable": "managed_services_unavailable",
            "current-job lifecycle verification is unavailable": "job_lifecycle_verification_unavailable",
            "instance unit state is unknown": "native_unit_state_unknown",
            "instance unit is active": "native_unit_active",
            "Lifecycle activation recovery failed; inspect job phase and current process state before retrying.": "activation_recovery_failed",
            "Lifecycle managed java verification failed; inspect job phase and current process state before retrying.": "managed_java_verification_failed",
            "Lifecycle materialization failed; inspect job phase and current process state before retrying.": "materialization_failed",
            "Lifecycle verified distribution install failed; inspect job phase and current process state before retrying.": "verified_distribution_install_failed",
            "Lifecycle activation failed; inspect job phase and current process state before retrying.": "activation_failed",
        }
        try:
            page = self.request("/api/v1/jobs/" + safe_id(identifier) + "/logs?after=0&limit=500")
        except Exception:
            return ["diagnostics_unavailable"]
        logs = page.get("logs", []) if isinstance(page, dict) else []
        if not isinstance(logs, list) or len(logs) > 500:
            return ["diagnostics_bound"]
        codes = set()
        for line in logs:
            text = line.get("message") if isinstance(line, dict) else None
            if isinstance(text, str) and len(text) <= 8192 and text in messages:
                codes.add(messages[text])
        return sorted(codes) or ["unclassified"]

    def console_diagnostics(self):
        # Read-only status, with a short independent deadline. Never retain the
        # native reason text, source fields, credentials, or raw response body.
        reasons = {
            "Fresh native readiness has not been verified.": "readiness_unverified",
            "Fresh native readiness could not be verified.": "readiness_unverified",
            "No managed JVM identity has been recorded.": "identity_absent",
            "The managed JVM is not running.": "process_stopped",
            "The managed JVM is stopping or has an unresolved control failure.": "process_control_unresolved",
            "The native server has not acknowledged world readiness.": "world_not_ready",
            "native management discovery or protocol version is unsupported": "discovery_unsupported",
            "native management discovery contains invalid methods": "discovery_invalid_methods",
            "native management discovery contains duplicate methods": "discovery_duplicate_methods",
            "authenticated native management connection failed": "management_connection_failed",
            "native readiness status method is unavailable": "status_method_unavailable",
            "native readiness result is invalid": "status_result_invalid",
            "a required native setting is unavailable": "required_setting_unavailable",
            "native gamerule discovery is unavailable": "gamerule_discovery_unavailable",
            "native gamerule response was invalid": "gamerule_result_invalid",
            "required native boolean rule is ambiguous or unsupported": "required_rule_unsupported",
            "required native rule was not discovered": "required_rule_missing",
            "required native setting differs from the desired value": "required_setting_mismatch",
            "native rule update is unavailable": "rule_update_unavailable",
            "native rule update returned an invalid acknowledgement": "rule_ack_invalid",
            "native rule update was not acknowledged exactly": "rule_ack_mismatch",
            "native rule readback did not match the desired value": "rule_readback_mismatch",
            "Fresh native readiness and required native rule values are verified. Plugin and mod gameplay overrides are unverified.": "native_ready",
        }
        try:
            response = self.request(PREFIX + "/console/status", timeout=5)
        except Exception:
            return {"diagnostic_available": False, "reason_code": "status_unavailable"}
        state = response.get("state") if isinstance(response, dict) else None
        if not isinstance(state, dict):
            return {"diagnostic_available": False, "reason_code": "status_malformed"}
        status = state.get("status")
        reason = state.get("reason")
        result = {"diagnostic_available": True,
                  "status": status if status in ("unknown", "stopped", "starting", "stopping", "running", "crashed", "settings_unverified", "ready") else "unclassified",
                  "reason_code": reasons.get(reason, "unclassified") if isinstance(reason, str) else "unclassified"}
        for key in ("process_running", "control_connected", "game_ready", "required_settings_verified", "ready_for_use", "logs_available"):
            if type(state.get(key)) is bool:
                result[key] = state[key]
        return result

    def lifecycle(self, operation, **kwargs):
        return self.job(self.request(PREFIX + "/lifecycle", {"operation": operation, **kwargs}, 202), 900 if operation == "install" else 300)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise Failure("api_redirect_refused")


class GameClient:
    def __init__(self, mode):
        self.process = subprocess.Popen(["node", str(HERE / "game-client/client.cjs"), "--mode", mode],
                       stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True,
                       encoding="utf-8")
        self.lines = queue.Queue(maxsize=8)
        self.closed = False
        self.thread = threading.Thread(target=self._read, daemon=True)
        self.thread.start()

    def _read(self):
        while True:
            line = self.process.stdout.readline(8193)
            if not line:
                value = None
            elif len(line) > 8192:
                value = {"event": "failure", "code": "client_output_bound"}
            else:
                try:
                    value = json.loads(line)
                except ValueError:
                    value = {"event": "failure", "code": "client_output_invalid"}
            try:
                self.lines.put(value, timeout=1)
            except queue.Full:
                return
            if value is None:
                return

    def ready(self):
        try:
            value = self.lines.get(timeout=65)
        except queue.Empty:
            raise Failure("client_readiness_timeout") from None
        require(isinstance(value, dict), "client_ended_before_ready")
        if value.get("event") == "failure":
            code = value.get("code")
            if not isinstance(code, str) or not re.fullmatch(r"client_[a-z_]{1,64}", code):
                code = "client_unclassified"
            raise Failure(code)
        require(value.get("event") == "ready" and value.get("version") == VERSION and
                value.get("protocol_client") == "minecraft-protocol@1.68.0", "client_readiness_invalid")
        require(all(type(value.get(k)) is int and 0 < value[k] < 100000 for k in
                    ("chunks", "teleports", "keepalives", "chunk_batches")), "client_packets_missing")
        require(type(value.get("initial_yaw")) in (int, float) and math.isfinite(value["initial_yaw"]), "client_yaw_invalid")
        return {key: value[key] for key in ("version", "protocol_client", "initial_yaw", "sent_yaw", "chunks", "teleports", "keepalives", "chunk_batches")}

    def alive(self):
        require(self.process.poll() is None and self.lines.empty(), "client_not_alive")

    def close(self):
        if self.closed:
            return
        self.closed = True
        try:
            self.process.stdin.write("close\n")
            self.process.stdin.flush()
            require(self.process.wait(timeout=8) == 0, "client_disconnect_failed")
        except (BrokenPipeError, subprocess.TimeoutExpired):
            self.process.kill()
            self.process.wait(timeout=8)
            raise Failure("client_disconnect_timeout") from None
        finally:
            self.process.stdin.close()
            self.process.stdout.close()


def yaw_matches(actual, expected):
    return isinstance(actual, (float, int)) and math.isfinite(actual) and abs((actual - expected + 180) % 360 - 180) < .01


class NBT:
    """Small bounded read-only NBT decoder for actual playerdata (no dependency)."""
    def __init__(self, raw):
        require(len(raw) <= 1 << 20, "nbt_size")
        self.raw = raw
        self.offset = 0
        self.nodes = 0

    def take(self, count):
        require(0 <= count <= len(self.raw) - self.offset, "nbt_truncated")
        value = self.raw[self.offset:self.offset + count]
        self.offset += count
        return value

    def number(self, fmt):
        return struct.unpack(">" + fmt, self.take(struct.calcsize(fmt)))[0]

    def string(self):
        return self.take(self.number("H")).decode("utf-8", errors="strict")

    def count(self):
        count = self.number("i")
        require(0 <= count <= 65536, "nbt_count")
        return count

    def tag(self, kind, depth=0):
        self.nodes += 1
        require(depth <= 32 and self.nodes <= 100000, "nbt_complexity")
        if kind in {1, 2, 3, 4, 5, 6}:
            return self.number({1: "b", 2: "h", 3: "i", 4: "q", 5: "f", 6: "d"}[kind])
        if kind == 7:
            return self.take(self.count())
        if kind == 8:
            return self.string()
        if kind == 9:
            item = self.number("B")
            return [self.tag(item, depth + 1) for _ in range(self.count())]
        if kind == 10:
            result = {}
            while True:
                item = self.number("B")
                if item == 0:
                    return result
                name = self.string()
                require(name not in result, "nbt_duplicate")
                result[name] = self.tag(item, depth + 1)
        if kind in {11, 12}:
            return [self.number("i" if kind == 11 else "q") for _ in range(self.count())]
        raise Failure("nbt_tag_invalid")


def player_yaw(raw):
    # gzip.decompress has no allocation bound; use a limited streaming read.
    with gzip.GzipFile(fileobj=io.BytesIO(raw)) as stream:
        unpacked = stream.read((1 << 20) + 1)
    parser = NBT(unpacked)
    require(parser.number("B") == 10, "nbt_root")
    parser.string()
    root = parser.tag(10)
    require(parser.offset == len(unpacked), "nbt_trailing")
    rotation = root.get("Rotation")
    require(isinstance(rotation, list) and len(rotation) == 2 and
            type(rotation[0]) in (int, float) and math.isfinite(rotation[0]), "nbt_rotation")
    return rotation[0]


def offline_uuid():
    value = bytearray(hashlib.md5(b"OfflinePlayer:AcceptanceBot", usedforsecurity=False).digest())
    value[6] = (value[6] & 15) | 48
    value[8] = (value[8] & 63) | 128
    return str(uuid.UUID(bytes=bytes(value)))


def archive_evidence(path, expected_yaw, online):
    """Verify real bytes against shipping manifest, without extracting entries."""
    with zipfile.ZipFile(path) as archive:
        entries = archive.infolist()
        require(0 < len(entries) <= 50000, "archive_entries")
        names = set()
        folded = set()
        total = 0
        for entry in entries:
            name = entry.filename
            pure = PurePosixPath(name)
            require(name.lower() not in folded, "archive_duplicate")
            require(not pure.is_absolute() and all(p not in {".", ".."} for p in pure.parts) and
                    str(pure) == name and "\\" not in name and ":" not in name and
                    not any(ord(c) < 32 for c in name), "archive_path")
            require(entry.file_size <= 256 << 20 and not entry.flag_bits & 1 and
                    (entry.external_attr >> 16) & 0o170000 in {0, 0o100000}, "archive_entry_type")
            names.add(name)
            folded.add(name.lower())
            total += entry.file_size
        require(total <= 512 << 20, "archive_expansion_bound")
        require("backup-manifest.json" in names, "archive_manifest_missing")
        require(archive.getinfo("backup-manifest.json").file_size <= 4 << 20, "archive_manifest_bound")
        manifest = json.loads(archive.read("backup-manifest.json"))
        require(manifest.get("instance_id") == INSTANCE and manifest.get("minecraft_version") == VERSION and
                manifest.get("kind") == "full" and manifest.get("world_present") is True, "archive_manifest_identity")
        require(manifest.get("capture_mode") == ("acknowledged_native_save_lease" if online else "stopped_native_admission"), "archive_capture_mode")
        listed = set()
        for entry in manifest["files"]:
            name = "server/" + entry["path"]
            require(name in names and name not in listed and SHA256.fullmatch(entry["sha256"]), "archive_manifest_entry")
            require(archive.getinfo(name).file_size == entry["size"], "archive_manifest_size")
            value = hashlib.sha256()
            with archive.open(name) as stream:
                for chunk in iter(lambda: stream.read(1 << 20), b""):
                    value.update(chunk)
            require(value.hexdigest() == entry["sha256"], "archive_manifest_hash")
            listed.add(name)
        require(names == listed | {"backup-manifest.json"}, "archive_unmanifested_entry")
        name = "server/world/playerdata/" + offline_uuid() + ".dat"
        require(name in names and "server/world/level.dat" in names, "archive_player_world_missing")
        require(archive.getinfo(name).file_size <= 1 << 20, "archive_player_bound")
        require(yaw_matches(player_yaw(archive.read(name)), expected_yaw), "archive_player_marker_mismatch")
        require(not any(n.endswith("/manager.db") or n == "server/manager.db" for n in names), "archive_contains_manager_db")
        return {"sha256": digest(path), "capture_mode": manifest["capture_mode"], "files_verified": len(listed),
                "player_marker_yaw": expected_yaw, "world_and_playerdata_verified": True}


def download_archive(api, record, destination):
    identifier = safe_id(record["id"])
    with api.open(PREFIX + "/backups/" + identifier + "/download") as response:
        expected = response.headers.get("X-Archive-SHA256", "")
        require(SHA256.fullmatch(expected) and response.headers.get_content_type() == "application/zip", "archive_download_headers")
        total = 0
        with open(destination, "xb") as stream:
            while True:
                chunk = response.read(1 << 20)
                if not chunk:
                    break
                total += len(chunk)
                require(total <= 512 << 20, "archive_download_bound")
                stream.write(chunk)
    require(digest(destination) == expected == record["checksum"]["value"], "archive_download_hash")


WINDOWS_SNAPSHOT_SCRIPT = r'''$ErrorActionPreference='Stop'
Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
using FILETIME = System.Runtime.InteropServices.ComTypes.FILETIME;
using System.Text;
public sealed class GameAcceptanceProcessSnapshot {
 public uint pid;
 public string image;
 public string created;
}
public static class GameAcceptanceNativeIdentity {
 [DllImport("kernel32.dll", SetLastError=true)] static extern IntPtr OpenProcess(uint access, bool inherit, uint pid);
 [DllImport("kernel32.dll", SetLastError=true)] static extern bool GetProcessTimes(IntPtr process, out FILETIME created, out FILETIME exited, out FILETIME kernel, out FILETIME user);
 [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern bool QueryFullProcessImageName(IntPtr process, uint flags, StringBuilder image, ref uint length);
 [DllImport("kernel32.dll", SetLastError=true)] static extern uint GetProcessId(IntPtr process);
 [DllImport("kernel32.dll", SetLastError=true)] static extern uint WaitForSingleObject(IntPtr handle, uint milliseconds);
 [DllImport("kernel32.dll", SetLastError=true)] static extern bool CloseHandle(IntPtr handle);
 public static GameAcceptanceProcessSnapshot Snapshot(uint pid) {
  IntPtr handle = OpenProcess(0x00101000, false, pid);
  if (handle == IntPtr.Zero) throw new InvalidOperationException("native_open");
  try {
   if (WaitForSingleObject(handle, 0) != 258 || GetProcessId(handle) != pid) throw new InvalidOperationException("native_binding");
   FILETIME created, exited, kernel, user;
   if (!GetProcessTimes(handle, out created, out exited, out kernel, out user)) throw new InvalidOperationException("native_times");
   var image = new StringBuilder(32768); uint length = 32768;
   if (!QueryFullProcessImageName(handle, 0, image, ref length)) throw new InvalidOperationException("native_image");
   if (WaitForSingleObject(handle, 0) != 258) throw new InvalidOperationException("native_exited");
   return new GameAcceptanceProcessSnapshot {
    pid = pid, image = image.ToString(),
    created = ((uint)created.dwHighDateTime).ToString("x8") + ((uint)created.dwLowDateTime).ToString("x8")
   };
  } finally { CloseHandle(handle); }
 }
}
'@
'''


def windows_native_identity(value, executable):
    # Query the original FILETIME from one pinned process handle. CIM datetime
    # has microsecond precision and cannot represent every native 100 ns tick.
    require(type(value["pid"]) is int and 1 < value["pid"] <= 0xffffffff, "native_identity_invalid")
    script = WINDOWS_SNAPSHOT_SCRIPT + "\n[GameAcceptanceNativeIdentity]::Snapshot(" + str(value["pid"]) + ") | ConvertTo-Json -Compress"
    result = subprocess.run(["pwsh", "-NoProfile", "-Command", script], capture_output=True, timeout=20)
    require(result.returncode == 0, "native_process_absent")
    process = json.loads(result.stdout)
    require(type(process.get("pid")) is int and process["pid"] == value["pid"], "native_pid_mismatch")
    require(os.path.samefile(process["image"], executable), "native_image_mismatch")
    native_start = process.get("created")
    require(isinstance(native_start, str) and re.fullmatch(r"[a-f0-9]{16}", native_start), "native_creation_invalid")
    require(value["start_identity"].endswith(":" + native_start), "native_start_mismatch")
    return native_start


def native_identity():
    path = DATA / "instances" / INSTANCE / "state/identity.json"
    value = bounded_json(path, 32768)
    require(value["instance_id"] == INSTANCE and type(value["pid"]) is int and value["pid"] > 1, "native_identity_invalid")
    require(isinstance(value["start_identity"], str) and re.fullmatch(r"[a-zA-Z0-9:{}.-]{1,128}", value["start_identity"]), "native_start_identity_invalid")
    require(isinstance(value["java_checksum"]["value"], str) and SHA256.fullmatch(value["java_checksum"]["value"]), "native_java_checksum_invalid")
    executable = Path(value["executable"])
    require(executable.is_relative_to(DATA / "runtimes"), "native_runtime_path")
    require(Path(value["working_dir"]) == DATA / "instances" / INSTANCE / "server", "native_working_directory")
    if os.name == "nt":
        native_start = windows_native_identity(value, executable)
    else:
        proc = Path("/proc") / str(value["pid"])
        require(os.path.samefile(proc / "exe", executable) and
                os.path.samefile(proc / "cwd", value["working_dir"]), "native_image_mismatch")
        native_start = (proc / "stat").read_text().rsplit(")", 1)[1].split()[19]
        boot = Path("/proc/sys/kernel/random/boot_id").read_text().strip()
        require(value["start_identity"] == boot + ":" + native_start, "native_start_mismatch")
    return {"pid": value["pid"], "start_identity": value["start_identity"], "native_start": native_start,
            "java_runtime_id": value["java_runtime_id"], "java_sha256": value["java_checksum"]["value"]}


def runtime_evidence(detail):
    identifier = detail["instance"]["java_runtime_id"]
    require(re.fullmatch(r"temurin-21-(?:linux|windows)-x64-[a-f0-9]{64}", identifier), "managed_runtime_identifier")
    manifest = bounded_json(DATA / "runtimes" / identifier / ".runtime.json", 8 << 20)
    runtime = manifest["runtime"]
    checksum = manifest["verified_sha256"]
    require(runtime["id"] == identifier and runtime["vendor"] == "Eclipse Adoptium" and runtime["major"] == 21 and
            runtime["artifact"]["provider"] == "adoptium" and SHA256.fullmatch(checksum) and
            runtime["artifact"]["checksum"] == {"algorithm": "sha256", "value": checksum} and
            runtime["artifact"]["source_url"].startswith("https://github.com/adoptium/temurin21-binaries/releases/download/"), "managed_runtime_provenance")
    require(isinstance(runtime["version"], str) and re.fullmatch(r"[0-9][A-Za-z0-9.+_-]{0,99}", runtime["version"]), "managed_runtime_version")
    datetime.datetime.fromisoformat(manifest["verified_at"].replace("Z", "+00:00"))
    return {"vendor": runtime["vendor"], "major": 21, "version": runtime["version"], "archive_sha256": checksum,
            "source": "official-adoptium-metadata-and-publisher-download", "verified_at": manifest["verified_at"]}


def lease_evidence(job_id, off, on):
    uri = (DATA / "manager.db").as_uri() + "?mode=ro"
    with sqlite3.connect(uri, uri=True, timeout=5) as connection:
        records = connection.execute("SELECT state,expires_at,updated_at FROM backup_leases WHERE instance_id=? AND job_id=?", (INSTANCE, job_id)).fetchall()
    require(len(records) == 1 and records[0][0] == "released", "backup_lease_not_released")
    expiry = datetime.datetime.fromisoformat(records[0][1].replace("Z", "+00:00"))
    released = datetime.datetime.fromisoformat(records[0][2].replace("Z", "+00:00"))
    require(released < expiry and off and on, "backup_save_ack_missing")
    return {"journal_state": "released", "released_before_deadline": True,
            "save_off_console_ack": True, "save_on_console_ack": True,
            "flush_ack_evidence": "successful-shipping-backup-and-acknowledged-native-save-lease-manifest"}


def console_save_evidence(api, cursor):
    deadline = time.monotonic() + 20
    off, on = False, False
    while time.monotonic() < deadline:
        page = api.request(PREFIX + "/console/logs?after=" + str(cursor) + "&limit=500")
        require(not page.get("gap"), "backup_console_gap")
        for line in page["lines"]:
            message = line.get("message", "")
            off = off or "Automatic saving is now disabled" in message
            on = on or "Automatic saving is now enabled" in message or "Saving is already turned on" in message
        cursor = page["next_cursor"]
        if off and on:
            return off, on
        time.sleep(1)
    return off, on


def wait_game(api):
    until = time.monotonic() + 180
    while time.monotonic() < until:
        state = api.request(PREFIX + "/console/status")["state"]
        if all(state.get(k) is True for k in ("process_running", "control_connected", "game_ready", "required_settings_verified", "ready_for_use")):
            return
        time.sleep(2)
    raise Failure("game_readiness_timeout", console_diagnostics=api.console_diagnostics())


def execute(output, report):
    api = API()
    client = None
    def phase(name):
        report["phase"] = name
        output.joinpath("game-summary.json").write_text(json.dumps(report, indent=2))
        print("PHASE " + name, flush=True)
    def connect(mode, expected=None):
        nonlocal client
        if client:
            client.close()
            client = None
            time.sleep(2)  # allow the server's asynchronous disconnect/save task
        client = GameClient(mode)
        evidence = client.ready()
        if expected is not None:
            require(yaw_matches(evidence["initial_yaw"], expected), "reconnect_marker_mismatch")
        report.setdefault("client_connections", []).append({"mode": mode, **evidence})
        return evidence
    try:
        phase("empty_setup")
        ready()
        require(api.request("/api/v1/auth/status")["needs_setup"] is True, "canonical_database_not_empty")
        setup = api.request("/api/v1/auth/setup", {"username": "game-acceptance-admin", "password": secrets.token_urlsafe(40)}, 201)
        require(setup["user"]["role"] == "admin", "setup_admin_role")
        me = api.request("/api/v1/auth/me")
        api.csrf = me.get("csrf_token")
        require(isinstance(api.csrf, str) and 0 < len(api.csrf) <= 256, "csrf_unavailable")
        require(me["user"]["id"] == setup["user"]["id"], "setup_session_identity")
        phase("instance_create")
        draft = {"instance": {"id": INSTANCE, "name": "Disposable game acceptance", "flavor": "vanilla", "minecraft_version": VERSION,
                 "java_major": 21, "memory_mib": 1536, "game_port": 25565}, "accept_eula": True,
                 "config": {"motd": "Disposable hosted acceptance", "difficulty": "peaceful", "game_mode": "creative", "max_players": 2,
                 "online_mode": False, "pvp": False, "view_distance": 2, "simulation_distance": 2, "level_name": "world", "level_seed": "33",
                 "extra": {"spawn-protection": "0", "enforce-secure-profile": "false"}}}
        detail = api.request("/api/v1/instances", draft, 201)["detail"]
        require(detail["eula"]["accepted"] is True, "eula_not_accepted")
        phase("official_resolve_install")
        preview = api.request(PREFIX + "/lifecycle/preview", {"flavor": "vanilla", "minecraft_version": VERSION})
        require(preview["selection"] == {"flavor": "vanilla", "minecraft_version": VERSION}, "preview_selection")
        require(preview["requirements"]["java_major"] == 21, "preview_java")
        preview_id = safe_id(preview["id"])
        artifacts = []
        for artifact in preview["artifacts"]:
            checksum = artifact["checksum"]
            require(checksum.get("algorithm") in {"sha1", "sha256"} and isinstance(checksum.get("value"), str) and
                    re.fullmatch(r"[a-f0-9]{40}|[a-f0-9]{64}", checksum["value"]) and
                    type(artifact.get("size")) is int and 0 < artifact["size"] <= 512 << 20, "preview_artifact_invalid")
            artifacts.append({"algorithm": checksum["algorithm"], "checksum": checksum["value"], "size": artifact["size"]})
        require(artifacts, "preview_artifacts_missing")
        report["server_artifacts"] = artifacts
        api.lifecycle("install", preview_id=preview_id)
        detail = api.request(PREFIX)["detail"]
        report["runtime"] = runtime_evidence(detail)
        phase("native_start_readiness")
        api.lifecycle("start")
        wait_game(api)
        report["first_native_identity"] = native_identity()
        phase("gameplay_marker_a")
        connect("mark-a")
        connect("observe", YAW_A)
        phase("online_backup")
        before = native_identity()
        old = {r["id"] for r in api.request(PREFIX + "/backups")["backups"]}
        cursor = api.request(PREFIX + "/console/logs?after=0&limit=500")["next_cursor"]
        backup_job = api.job(api.request(PREFIX + "/backups", {"kind": "full"}, 202), 240)
        require(native_identity() == before, "online_backup_replaced_game")
        client.alive()
        off, on = console_save_evidence(api, cursor)
        report["online_lease"] = lease_evidence(backup_job["id"], off, on)
        records = [r for r in api.request(PREFIX + "/backups")["backups"] if r["id"] not in old]
        require(len(records) == 1 and records[0]["state"] == "ready" and records[0]["protected"] is True and records[0]["trigger"] == "manual", "online_backup_record")
        backup = records[0]
        with tempfile.TemporaryDirectory(prefix="minecraft-game-private-") as scratch:
            archive = Path(scratch) / "online.zip"
            download_archive(api, backup, archive)
            report["online_backup"] = archive_evidence(archive, YAW_A, True)
            report["online_backup"]["native_identity_unchanged"] = True
            phase("gameplay_marker_b")
            connect("mark-b")
            connect("observe", YAW_B)
            client.close()
            client = None
            phase("deliberate_stop_restore")
            api.lifecycle("stop", emergency=True, confirmation=INSTANCE)
            require(api.request(PREFIX + "/lifecycle")["process_running"] is False, "stop_process_running")
            old = {r["id"] for r in api.request(PREFIX + "/backups")["backups"]}
            api.job(api.request(PREFIX + "/backups/" + safe_id(backup["id"]) + "/restore", {"confirmed": True}, 202), 300)
            safety = [r for r in api.request(PREFIX + "/backups")["backups"] if r["id"] not in old and r["trigger"] == "safety"]
            require(len(safety) == 1 and safety[0]["protected"] is True and safety[0]["state"] == "ready", "fresh_restore_safety_missing")
            safety_path = Path(scratch) / "safety.zip"
            download_archive(api, safety[0], safety_path)
            report["restore_safety_backup"] = archive_evidence(safety_path, YAW_B, False)
            require(report["restore_safety_backup"]["sha256"] != report["online_backup"]["sha256"], "safety_archive_not_distinct")
        phase("restored_reconnect")
        api.lifecycle("start")
        wait_game(api)
        connect("observe", YAW_A)
        client.close()
        client = None
        phase("manager_restart")
        api.lifecycle("autostart", enabled=True)
        before = native_identity()
        control("restart")
        ready()
        require(api.request("/api/v1/auth/me")["user"]["id"] == setup["user"]["id"], "restart_session_lost")
        wait_game(api)
        after = native_identity()
        if os.name == "nt":
            require(after != before, "windows_restart_reused_game")
        else:
            require(after == before, "linux_restart_stopped_game")
        connect("observe", YAW_A)
        client.close()
        client = None
        report["manager_restart"] = {"linux_identity_preserved": os.name != "nt", "windows_autostart_new_identity": os.name == "nt", "restored_marker_preserved": True}
        phase("deliberate_stop_no_restart")
        api.lifecycle("stop", emergency=True, confirmation=INSTANCE)
        # Scheduler's first crash retry would occur inside this observation.
        until = time.monotonic() + 65
        while time.monotonic() < until:
            require(api.request(PREFIX + "/lifecycle")["process_running"] is False, "deliberate_stop_restarted")
            time.sleep(2)
        report["deliberate_stop_no_restart_seconds"] = 65
        report["passed"] = True
        phase("complete")
    finally:
        if client:
            client.close()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--minecraft-version", choices=[VERSION], required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    hosted()
    output = safe_directory(args.output)
    report = {"scope": "vanilla-1.21.9-real-headless-offline-loopback-client", "passed": False,
              "phase": "preflight", "minecraft_version": VERSION, "os": "windows" if os.name == "nt" else "linux",
              "fixtures": ["disposable-empty-canonical-state", "eula-explicitly-accepted", "offline-auth-loopback-firewall", "creative-peaceful-world-seed-33", "player-view-yaw-marker"],
              "not_qualified": ["Microsoft-authenticated-client", "graphical-client", "other-version-flavor-host-tuples", "full-map-addons", "crash-retry-injection"]}
    try:
        execute(output, report)
    except Exception as error:
        report["failure"] = {"code": error.code, **error.evidence} if isinstance(error, Failure) else {"code": "unexpected_" + type(error).__name__}
        print("FAIL game acceptance phase=" + report["phase"] + " code=" + report["failure"]["code"], file=sys.stderr)
    finally:
        output.joinpath("game-summary.json").write_text(json.dumps(report, indent=2))
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Failure as error:
        print("FAIL game acceptance preflight code=" + error.code, file=sys.stderr)
        sys.exit(2)

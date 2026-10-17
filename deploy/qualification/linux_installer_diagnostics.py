#!/usr/bin/env python3
"""Emit small allowlisted service and installer-stage diagnostics."""

import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys


UNIT = re.compile(r"(?:minecraft-ui\.service|minecraft-ui-update-recovery\.service|minecraft-ui-install-[0-9a-f]{64}\.service)\Z")
INSTALL_UNIT = re.compile(r"minecraft-ui-install-([0-9a-f]{64})\.service\Z")
STAGES = ("prepared", "quiescing", "stopped", "replacing", "published", "verifying",
          "rolling_back", "rolled_back", "committed", "manual_repair", "startup_gate",
          "preflight", "prepare", "record_prepared", "quiesce", "record_quiescing",
          "record_stopped", "record_replacing", "replace", "publish", "record_published",
          "record_verifying", "verify", "record_committed", "finish", "record_rollback",
          "rollback", "record_rolled_back", "record_manual_repair", "recover", "worker")
OUTCOMES = {"pending", "unchanged", "installed", "uninstalled", "rolled_back", "manual_repair"}
RESULT_FIELDS = {"outcome", "operation_id", "image", "failed_stage"}
FAILED_STAGES = {
    "preflight", "prepare", "record_prepared", "quiesce", "record_quiescing",
    "record_stopped", "record_replacing", "replace", "publish", "record_published",
    "record_verifying", "verify", "record_committed", "finish", "record_rollback",
    "rollback", "record_rolled_back", "record_manual_repair", "recover", "worker",
}
ENTRY_FAILURES = {
    "native manager operation refused: installer worker could not acquire its protected session":
        ("protected_session_unavailable", "preflight"),
    "native manager operation refused: installer worker transaction does not match":
        ("worker_transaction_mismatch", "recover"),
    "native manager operation refused: installer worker executable is not authorized":
        ("worker_executable_unauthorized", "preflight"),
    "native manager operation refused: installer worker transaction is not prepared":
        ("worker_transaction_not_prepared", "recover"),
}
PROPERTIES = ("Id", "ActiveState", "SubState", "Result", "MainPID", "ExecMainStatus")
MAX_JOURNAL_BYTES = 1_000_000
MAX_RESULT_MESSAGE_BYTES = 16_384
MAX_WORKER_RESULTS = 32
MAX_PAYLOAD_BYTES = 32768
MAX_INSTALL_OPERATIONS = 16
MAX_INSTALL_ENTRIES = 64
HEX64 = re.compile(r"[0-9a-f]{64}\Z")
VERSION = re.compile(r"v[0-9]+\.[0-9]+\.[0-9]+\Z")

UPDATE_STATE_SLOTS = (
    "installation-transaction.json", "installation-transaction.pending",
    "installation-launch.json", "installation-launch.pending",
    "installation.json", "installation.json.pending",
    "activation.json", "activation.pending", "installed.json", "installed.pending",
    "launch.json", "launch.pending", "installation-receipt.pending", "request.json",
)
OPERATION_SLOTS = (
    "intent.json", "executor", "effects.json", "effects.pending", "result.json",
    "result.json.pending", "inputs", "preimage", "source-bootstrap-trust",
)
BIN_SLOTS = ("minecraft-ui.previous", "minecraft-ui.next")
CONTROL_SLOTS = (
    "challenge.json", "drained.json", "drained.pending", "request.json",
    "maintenance-challenge.json", "maintenance-drained.json", "maintenance-drained.pending",
)


def run(argv, timeout=20):
    return subprocess.run(argv, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                          stderr=subprocess.DEVNULL, timeout=timeout, check=False)


def unit_summaries():
    listed = run(["systemctl", "list-units", "--all", "--plain", "--no-legend", "--no-pager",
                  "minecraft-ui.service", "minecraft-ui-update-recovery.service", "minecraft-ui-install-*.service"])
    units = {"minecraft-ui.service", "minecraft-ui-update-recovery.service"}
    for line in listed.stdout.decode("utf-8", "replace").splitlines():
        name = line.split(maxsplit=1)[0] if line.split() else ""
        if UNIT.fullmatch(name):
            units.add(name)
    result = []
    for unit in sorted(units):
        if not UNIT.fullmatch(unit):
            continue
        shown = run(["systemctl", "show", unit, *("--property=" + name for name in PROPERTIES)])
        values = {}
        for line in shown.stdout.decode("utf-8", "replace").splitlines():
            key, separator, value = line.partition("=")
            if separator and key in PROPERTIES:
                values[key] = value[:96]
        result.append({"unit": unit, "properties": values})
    return result


def journal_stages():
    completed = run(["journalctl", "--lines=300", "-u", "minecraft-ui.service", "-u", "minecraft-ui-update-recovery.service",
                     "-u", "minecraft-ui-install-*.service", "--no-pager", "-o", "json"], timeout=30)
    if len(completed.stdout) > MAX_JOURNAL_BYTES:
        return {"bounded": False, "events": [], "worker_results": [], "entry_failures": []}
    return summarize_journal(completed.stdout.splitlines())


def summarize_journal(lines):
    events = {}
    worker_results = {}
    entry_failures = {}
    for line in lines:
        try:
            item = json.loads(line)
        except (UnicodeDecodeError, json.JSONDecodeError):
            continue
        if not isinstance(item, dict):
            continue
        unit = item.get("_SYSTEMD_UNIT", "")
        if not isinstance(unit, str) or not UNIT.fullmatch(unit):
            continue
        priority = item.get("PRIORITY", "")
        if not isinstance(priority, str) or not priority.isdecimal():
            priority = "unknown"
        message = item.get("MESSAGE", "")
        if not isinstance(message, str):
            message = ""
        lower = message.lower()
        result = safe_installer_result(message)
        install_unit = INSTALL_UNIT.fullmatch(unit)
        if (result is not None and (install_unit is None
                                    or result.get("operation_id") != install_unit.group(1))):
            result = None
        entry_failure = ENTRY_FAILURES.get(message) if install_unit is not None else None
        if result is not None:
            stage = result["stage"] or next((name for name in STAGES if name in lower), "other")
            image = result.get("image")
            image_key = None if image is None else (image["version"], image["schema"], image["sha256"])
            result_key = (unit, result["outcome"], result["stage"], result.get("operation_id"), image_key)
            worker_results[result_key] = min(worker_results.get(result_key, 0) + 1, 999)
        elif entry_failure is not None:
            _, stage = entry_failure
        else:
            stage = next((name for name in STAGES if name in lower), "other")
        if entry_failure is not None:
            category, failed_stage = entry_failure
            key = (unit, category, failed_stage)
            entry_failures[key] = min(entry_failures.get(key, 0) + 1, 999)
        key = (unit, priority, stage)
        events[key] = min(events.get(key, 0) + 1, 999)
    return {"bounded": True, "events": [
        {"unit": unit, "priority": priority, "stage": stage, "count": count}
        for (unit, priority, stage), count in sorted(events.items())[:128]
    ], "worker_results": [
        {
            **{"unit": unit, "outcome": outcome, "stage": stage, "count": count},
            **({"operation_id": operation_id} if operation_id is not None else {}),
            **({"image": {"version": image[0], "schema": image[1], "sha256": image[2]}}
               if image is not None else {}),
        }
        for (unit, outcome, stage, operation_id, image), count in sorted(
            worker_results.items(),
            key=lambda entry: json.dumps(entry[0], sort_keys=True, separators=(",", ":")),
        )[:MAX_WORKER_RESULTS]
    ], "entry_failures": [
        {"unit": unit, "category": category, "stage": stage, "count": count}
        for (unit, category, stage), count in sorted(entry_failures.items())[:MAX_WORKER_RESULTS]
    ]}


def _unique_json_object(pairs):
    value = {}
    for key, member in pairs:
        if key in value:
            raise ValueError("duplicate JSON member")
        value[key] = member
    return value


def safe_installer_result(message):
    """Return only validated, non-secret fields from an exact worker JSON message."""
    try:
        if len(message.encode("utf-8", "strict")) > MAX_RESULT_MESSAGE_BYTES:
            return None
    except UnicodeError:
        return None
    try:
        value = json.loads(message, object_pairs_hook=_unique_json_object)
    except (UnicodeError, json.JSONDecodeError, ValueError, TypeError):
        return None
    if not isinstance(value, dict) or set(value) - RESULT_FIELDS:
        return None
    outcome = value.get("outcome")
    if not isinstance(outcome, str) or outcome not in OUTCOMES:
        return None
    safe = {"outcome": outcome, "stage": None}
    operation_id = value.get("operation_id")
    if operation_id is not None:
        if not isinstance(operation_id, str) or HEX64.fullmatch(operation_id) is None:
            return None
        safe["operation_id"] = operation_id
    stage = value.get("failed_stage")
    if stage is not None:
        if not isinstance(stage, str) or stage not in FAILED_STAGES:
            return None
        safe["stage"] = stage
    image = value.get("image")
    if image is not None:
        if (not isinstance(image, dict) or set(image) != {"version", "schema", "sha256"}
                or not isinstance(image["version"], str) or VERSION.fullmatch(image["version"]) is None
                or type(image["schema"]) is not int or image["schema"] <= 0
                or not isinstance(image["sha256"], str) or HEX64.fullmatch(image["sha256"]) is None):
            return None
        safe["image"] = {"version": image["version"], "schema": image["schema"],
                         "sha256": image["sha256"]}
    return safe


def _kind(mode):
    if stat.S_ISREG(mode):
        return "file"
    if stat.S_ISDIR(mode):
        return "directory"
    if stat.S_ISLNK(mode):
        return "symlink"
    if stat.S_ISSOCK(mode):
        return "socket"
    if stat.S_ISFIFO(mode):
        return "fifo"
    if stat.S_ISCHR(mode) or stat.S_ISBLK(mode):
        return "device"
    return "other"


def _bounded_integer(value):
    return max(0, min(int(value), (1 << 63) - 1))


def _metadata_at(directory_fd, name):
    """Describe a fixed child using fstatat semantics; never follows a symlink."""
    try:
        info = os.stat(name, dir_fd=directory_fd, follow_symlinks=False)
    except FileNotFoundError:
        return {"state": "absent"}
    except OSError:
        return {"state": "unavailable"}
    return {
        "state": "present", "kind": _kind(info.st_mode), "mode": _bounded_integer(stat.S_IMODE(info.st_mode)),
        "uid": _bounded_integer(info.st_uid), "gid": _bounded_integer(info.st_gid),
        "nlink": _bounded_integer(info.st_nlink), "size": _bounded_integer(info.st_size),
    }


def _open_directory(path, protected_uid=None):
    """Open a fixed directory chain with O_NOFOLLOW and optionally validate its leaf."""
    candidate = Path(path)
    if not candidate.is_absolute() or any(part in (".", "..") for part in candidate.parts[1:]):
        return None
    flags = os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        current_fd = os.open(candidate.anchor, flags)
    except OSError:
        return None
    for part in candidate.parts[1:]:
        try:
            child_fd = os.open(part, flags, dir_fd=current_fd)
        except OSError:
            os.close(current_fd)
            return None
        os.close(current_fd)
        current_fd = child_fd
    try:
        info = os.fstat(current_fd)
    except OSError:
        os.close(current_fd)
        return None
    if not stat.S_ISDIR(info.st_mode) or (protected_uid is not None and
            (info.st_uid != protected_uid or stat.S_IMODE(info.st_mode) != 0o700)):
        os.close(current_fd)
        return None
    return current_fd


def _directory_entries(directory_fd, limit):
    """Return bounded entries internally; callers must not serialize unknown names."""
    if limit <= 0:
        return [], True
    try:
        with os.scandir(directory_fd) as iterator:
            entries = []
            for entry in iterator:
                entries.append(entry.name)
                if len(entries) >= limit:
                    return entries, True
            return entries, False
    except OSError:
        return None, False


def _directory_snapshot(path, slots, entry_budget=None, protected_uid=None):
    directory_fd = _open_directory(path, protected_uid=protected_uid)
    if directory_fd is None:
        return {"state": "refused"}
    try:
        result = {"state": "present", "slots": {name: _metadata_at(directory_fd, name) for name in slots}}
        if entry_budget is not None:
            names, truncated = _directory_entries(directory_fd, entry_budget)
            if names is None:
                result["entries_state"] = "unavailable"
                result["unknown_entries"] = 0
            else:
                known = set(slots)
                result["unknown_entries"] = min(sum(name not in known for name in names), entry_budget)
                result["entries_truncated"] = bool(truncated or len(names) > entry_budget)
        return result
    finally:
        os.close(directory_fd)


def _presence_snapshot(path, slots):
    directory_fd = _open_directory(path)
    if directory_fd is None:
        return {"state": "refused", "slots": {}}
    try:
        values = {name: _metadata_at(directory_fd, name)["state"] for name in slots}
        return {"state": "present", "slots": values}
    finally:
        os.close(directory_fd)


def _operation_directory_info(directory_fd):
    return os.fstat(directory_fd)


def _protected_state_snapshot(state_root="/var/lib/minecraft-ui-update",
                              install_root="/var/lib/minecraft-ui-update/install",
                              bin_root="/usr/local/bin", expected_uid=0,
                              data_root="/var/lib/minecraft-ui"):
    """Fixed-path, metadata-only snapshot; injectable roots are private for fixtures."""
    state = _directory_snapshot(state_root, UPDATE_STATE_SLOTS,
                                protected_uid=expected_uid)
    install = {"state": "refused", "operations": [], "unknown_entries": 0,
               "truncated": False}
    entries_seen = 0
    install_fd = _open_directory(install_root, protected_uid=expected_uid)
    if install_fd is not None:
        names, truncated = _directory_entries(install_fd, MAX_INSTALL_ENTRIES)
        if names is not None:
            operation_ids = [name for name in names if HEX64.fullmatch(name)]
            entries_seen = len(names)
            unknown = len(names) - len(operation_ids)
            op_results = []
            entry_overflow = False
            for operation_id in operation_ids[:MAX_INSTALL_OPERATIONS]:
                try:
                    operation_fd = os.open(operation_id,
                                           os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) |
                                           getattr(os, "O_NOFOLLOW", 0), dir_fd=install_fd)
                except OSError:
                    op_results.append({"id": operation_id, "state": "refused"})
                    continue
                try:
                    operation_info = _operation_directory_info(operation_fd)
                except OSError:
                    os.close(operation_fd)
                    op_results.append({"id": operation_id, "state": "refused"})
                    continue
                if (not stat.S_ISDIR(operation_info.st_mode) or operation_info.st_uid != expected_uid
                        or stat.S_IMODE(operation_info.st_mode) != 0o700):
                    os.close(operation_fd)
                    op_results.append({"id": operation_id, "state": "refused"})
                    continue
                slots = {name: _metadata_at(operation_fd, name) for name in OPERATION_SLOTS}
                remaining = max(0, MAX_INSTALL_ENTRIES - entries_seen)
                if remaining == 0:
                    entry_overflow = True
                    os.close(operation_fd)
                    op_results.append({"id": operation_id, "state": "present", "slots": slots,
                                       "entries_state": "not_enumerated"})
                    continue
                op_entries, op_truncated = _directory_entries(operation_fd, remaining)
                if op_entries is not None:
                    consumed = min(len(op_entries), remaining)
                    entries_seen += consumed
                    unknown += sum(name not in OPERATION_SLOTS for name in op_entries[:consumed])
                    entry_overflow = entry_overflow or op_truncated
                else:
                    os.close(operation_fd)
                    op_results.append({"id": operation_id, "state": "present", "slots": slots,
                                       "entries_state": "unavailable"})
                    continue
                os.close(operation_fd)
                op_results.append({"id": operation_id, "state": "present", "slots": slots,
                                   "entries_state": "present"})
            install = {"state": "present", "operations": op_results,
                       "unknown_entries": _bounded_integer(unknown),
                       "truncated": bool(truncated or len(operation_ids) > MAX_INSTALL_OPERATIONS
                                          or entry_overflow)}
        else:
            install = {"state": "unavailable", "operations": [], "unknown_entries": 0,
                       "truncated": False}
        os.close(install_fd)
    binary = _directory_snapshot(bin_root, BIN_SLOTS)
    binary_fd = _open_directory(bin_root)
    operation_scratch = []
    if binary_fd is not None:
        try:
            for item in install.get("operations", []):
                operation_id = item.get("id")
                if isinstance(operation_id, str) and HEX64.fullmatch(operation_id):
                    scratch_name = "minecraft-ui.install-" + operation_id
                    operation_scratch.append({"id": operation_id,
                                              "slot": scratch_name,
                                              "metadata": _metadata_at(binary_fd, scratch_name)})
            if binary.get("state") == "present":
                names, truncated = _directory_entries(binary_fd, 64)
                if names is not None:
                    binary["unknown_entries"] = _bounded_integer(
                        sum(name not in BIN_SLOTS for name in names))
                    binary["unknown_minecraft_ui_entries"] = _bounded_integer(
                        sum(name.startswith("minecraft-ui.") and name not in BIN_SLOTS for name in names))
                    binary["entries_truncated"] = bool(truncated)
        finally:
            os.close(binary_fd)
    control = _presence_snapshot(Path(data_root) / "selfupdate" / "control", CONTROL_SLOTS)
    return {"state": state, "install": install, "bin_scratch": binary,
            "operation_scratch": operation_scratch, "control_presence": control}


def _bounded_payload(payload):
    encoded = json.dumps(payload, sort_keys=True, separators=(",", ":")) + "\n"
    if len(encoded.encode("utf-8")) > MAX_PAYLOAD_BYTES:
        payload["units"] = payload.get("units", [])[:16]
        payload["journal"] = {"bounded": False, "events": [], "worker_results": [], "entry_failures": []}
        payload.pop("protected_state", None)
        encoded = json.dumps(payload, sort_keys=True, separators=(",", ":")) + "\n"
    if len(encoded.encode("utf-8")) > MAX_PAYLOAD_BYTES:
        payload["units"] = []
        encoded = json.dumps(payload, sort_keys=True, separators=(",", ":")) + "\n"
    if len(encoded.encode("utf-8")) > MAX_PAYLOAD_BYTES:
        payload = {"format": 1, "scope": "bounded-diagnostics-omitted", "units": [],
                   "journal": {"bounded": False, "events": [], "worker_results": [], "entry_failures": []}}
        encoded = json.dumps(payload, sort_keys=True, separators=(",", ":")) + "\n"
    return encoded


def main():
    if len(sys.argv) != 2:
        return 2
    output = Path(sys.argv[1])
    payload = {"format": 1, "scope": "fixed-installer-units-scrubbed-journal-and-protected-state-metadata",
               "units": unit_summaries(), "journal": journal_stages(),
               "protected_state": _protected_state_snapshot()}
    encoded = _bounded_payload(payload)
    (output / "diagnostics.json").write_text(encoded, encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

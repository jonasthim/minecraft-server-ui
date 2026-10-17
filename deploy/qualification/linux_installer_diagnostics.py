#!/usr/bin/env python3
"""Emit small allowlisted service and installer-stage diagnostics."""

import json
from pathlib import Path
import re
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
HEX64 = re.compile(r"[0-9a-f]{64}\Z")
VERSION = re.compile(r"v[0-9]+\.[0-9]+\.[0-9]+\Z")


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


def main():
    if len(sys.argv) != 2:
        return 2
    output = Path(sys.argv[1])
    payload = {"format": 1, "scope": "fixed-installer-units-and-scrubbed-journal-stages",
               "units": unit_summaries(), "journal": journal_stages()}
    encoded = json.dumps(payload, sort_keys=True, separators=(",", ":")) + "\n"
    if len(encoded) > 32768:
        payload["units"] = payload["units"][:16]
        payload["journal"] = {"bounded": False, "events": [], "worker_results": [], "entry_failures": []}
        encoded = json.dumps(payload, sort_keys=True, separators=(",", ":")) + "\n"
    (output / "diagnostics.json").write_text(encoded, encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

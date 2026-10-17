#!/usr/bin/env python3
"""Emit small allowlisted service and installer-stage diagnostics."""

import json
from pathlib import Path
import re
import subprocess
import sys


UNIT = re.compile(r"(?:minecraft-ui\.service|minecraft-ui-update-recovery\.service|minecraft-ui-install-[0-9a-f]{64}\.service)\Z")
STAGES = ("prepared", "quiescing", "stopped", "replacing", "published", "verifying",
          "rolling_back", "rolled_back", "committed", "manual_repair", "startup_gate")
PROPERTIES = ("Id", "ActiveState", "SubState", "Result", "MainPID", "ExecMainStatus")
MAX_JOURNAL_BYTES = 1_000_000


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
        return {"bounded": False, "events": []}
    return summarize_journal(completed.stdout.splitlines())


def summarize_journal(lines):
    events = {}
    for line in lines:
        try:
            item = json.loads(line)
        except (UnicodeDecodeError, json.JSONDecodeError):
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
        stage = next((name for name in STAGES if name in lower), "other")
        key = (unit, priority, stage)
        events[key] = min(events.get(key, 0) + 1, 999)
    return {"bounded": True, "events": [
        {"unit": unit, "priority": priority, "stage": stage, "count": count}
        for (unit, priority, stage), count in sorted(events.items())[:128]
    ]}


def main():
    if len(sys.argv) != 2:
        return 2
    output = Path(sys.argv[1])
    payload = {"format": 1, "scope": "fixed-installer-units-and-scrubbed-journal-stages",
               "units": unit_summaries(), "journal": journal_stages()}
    encoded = json.dumps(payload, sort_keys=True, separators=(",", ":")) + "\n"
    if len(encoded) > 32768:
        payload["units"] = payload["units"][:16]
        payload["journal"] = {"bounded": False, "events": []}
        encoded = json.dumps(payload, sort_keys=True, separators=(",", ":")) + "\n"
    (output / "diagnostics.json").write_text(encoded, encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

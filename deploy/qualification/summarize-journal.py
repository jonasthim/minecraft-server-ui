#!/usr/bin/env python3
"""Turn raw journald input into counts and fixed identifiers, never messages."""
from collections import Counter
import json
import sys

priorities = Counter()
known_events = Counter()
native_update_failures = Counter()
schema_probe_failures = Counter()
activation_stages = {
    "candidate_fence", "journal_absence", "prior_hash", "prepared_journal",
    "challenge_publish", "manager_stop", "manager_lock", "drain_read",
    "drain_validate", "schema_probe", "schema_validate", "stopped_journal",
    "scratch_check", "previous_copy", "candidate_copy", "executable_fence",
    "candidate_probe", "candidate_validate", "candidate_hash",
}
failure_markers = {"native-update-failure:" + stage: stage for stage in activation_stages}


def schema_probe_marker(message):
    prefix = "native-update-schema-diagnostic:"
    if not message.startswith(prefix):
        return None
    marker = message[len(prefix):]
    fields = marker.split(":")
    if len(fields) not in (2, 3):
        return None
    stage, reason = fields[:2]
    if len(fields) == 3:
        raw = fields[2]
        if not (raw.isascii() and raw.isdecimal() and 0 < len(raw) <= 5):
            return None
        code = int(raw)
        if str(code) != raw:
            return None
        if stage == "command":
            limit = {"exit": 255, "signal": 64}.get(reason, 0)
        elif stage in {"config", "schema_read", "challenge_read", "challenge_version", "actor_read", "encode"}:
            limit = 65535 if reason == "sqlite" else 0
        else:
            return None
        return marker if 0 < code <= limit else None
    if stage == "command":
        allowed = {"launch_permission", "launch_not_exist", "launch_other", "context_canceled",
                   "context_deadline", "output_overflow", "stderr_overflow", "invalid_json"}
    elif stage in {"config", "schema_read", "challenge_read", "challenge_version", "actor_read", "encode"}:
        allowed = {"permission", "not_exist", "canceled", "deadline", "other"}
    else:
        return None
    return marker if reason in allowed else None


for line in sys.stdin:
    try:
        entry = json.loads(line)
    except ValueError:
        continue
    priority = str(entry.get("PRIORITY", "unknown"))
    priorities[priority if priority in tuple(map(str, range(8))) else "unknown"] += 1
    message = entry.get("MESSAGE", "")
    if isinstance(message, str):
        # Exact fixed messages only: never retain an arbitrary suffix, stage,
        # error, path, request or other journald fields.
        if message in failure_markers:
            native_update_failures[failure_markers[message]] += 1
        schema_marker = schema_probe_marker(message)
        if schema_marker is not None:
            schema_probe_failures[schema_marker] += 1
        for fixed in ("Minecraft manager listening", "minecraft-ui stopped", "Failed to start", "Stopped Minecraft Server UI manager"):
            if fixed in message:
                known_events[fixed] += 1
print(json.dumps({"priority_counts": dict(priorities), "known_event_counts": dict(known_events),
                  "native_update_failure_counts": dict(native_update_failures),
                  "native_schema_probe_counts": dict(schema_probe_failures)}))

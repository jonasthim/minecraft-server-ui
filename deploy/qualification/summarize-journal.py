#!/usr/bin/env python3
"""Turn raw journald input into counts and fixed identifiers, never messages."""
from collections import Counter
import json
import sys

priorities = Counter()
known_events = Counter()
native_update_failures = Counter()
activation_stages = {
    "candidate_fence", "journal_absence", "prior_hash", "prepared_journal",
    "challenge_publish", "manager_stop", "manager_lock", "drain_read",
    "drain_validate", "schema_probe", "schema_validate", "stopped_journal",
    "scratch_check", "previous_copy", "candidate_copy", "executable_fence",
    "candidate_probe", "candidate_validate", "candidate_hash",
}
failure_markers = {"native-update-failure:" + stage: stage for stage in activation_stages}
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
        for fixed in ("Minecraft manager listening", "minecraft-ui stopped", "Failed to start", "Stopped Minecraft Server UI manager"):
            if fixed in message:
                known_events[fixed] += 1
print(json.dumps({"priority_counts": dict(priorities), "known_event_counts": dict(known_events),
                  "native_update_failure_counts": dict(native_update_failures)}))

#!/usr/bin/env python3
"""Turn raw journald input into counts and fixed identifiers, never messages."""
from collections import Counter
import json
import sys

priorities = Counter()
known_events = Counter()
for line in sys.stdin:
    try:
        entry = json.loads(line)
    except ValueError:
        continue
    priority = str(entry.get("PRIORITY", "unknown"))
    priorities[priority if priority in tuple(map(str, range(8))) else "unknown"] += 1
    message = entry.get("MESSAGE", "")
    if isinstance(message, str):
        for fixed in ("Minecraft manager listening", "minecraft-ui stopped", "Failed to start", "Stopped Minecraft Server UI manager"):
            if fixed in message:
                known_events[fixed] += 1
print(json.dumps({"priority_counts": dict(priorities), "known_event_counts": dict(known_events)}))

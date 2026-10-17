#!/usr/bin/env python3
"""Turn raw journald input into counts and fixed identifiers, never messages."""
from collections import Counter
import json
import sys

priorities = Counter()
known_events = Counter()
native_update_failures = Counter()
schema_probe_failures = Counter()
pam_failures = Counter()
errno_reasons = {
    "Operation not permitted": "eperm", "Permission denied": "eacces", "No such file or directory": "enoent",
    "Not a directory": "enotdir", "Exec format error": "enoexec", "Text file busy": "etxtbsy",
    "Argument list too long": "e2big", "Resource temporarily unavailable": "eagain", "Cannot allocate memory": "enomem",
    "Too many open files": "emfile", "Too many open files in system": "enfile", "Invalid argument": "einval",
    "Read-only file system": "erofs",
}
pam_reasons = {
    "abort", "module_load", "symbol", "service", "system", "memory", "permission", "authentication",
    "credentials_insufficient", "authinfo", "unknown_user", "retries", "new_token", "expired_account", "session",
    "credentials_unavailable", "credentials_expired", "credentials", "module_data", "bad_item", "conversation",
    "token", "token_recovery", "token_busy", "token_aging", "retry", "ignored", "unknown_module", "expired_token",
    "conversation_again", "incomplete", "unknown",
}
pam_messages = {
    "pam_limits(runuser:session): cannot initialize": "limits:initialize:other",
    "pam_unix(runuser:session): open_session - error recovering username": "unix:username:other",
    "pam_unix(runuser:session): open_session - error recovering service": "unix:service:other",
}
for error_text, code in errno_reasons.items():
    for resource in ("cpu", "fsize", "data", "stack", "core", "rss", "nproc", "nofile", "memlock",
                     "as", "locks", "sigpending", "msgqueue", "nice", "rtprio"):
        pam_messages[f"pam_limits(runuser:session): Could not set limit for '{resource}': {error_text}"] = f"limits:{resource}:{code}"
    pam_messages[f"pam_limits(runuser:session): Could not set limit for PRIO_PROCESS: {error_text}"] = f"limits:priority:{code}"
    pam_messages[f"pam_limits(runuser:session): Could not set prctl(PR_SET_NO_NEW_PRIVS): {error_text}"] = f"limits:nonewprivs:{code}"
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
    elif stage == "child":
        allowed = {"entered"}
    elif stage in {"runuser_auth", "runuser_session", "runuser_credentials"}:
        allowed = pam_reasons
    elif stage in {"runuser_groups", "runuser_gid", "runuser_uid", "runuser_environment",
                   "runuser_signal", "runuser_fork", "runuser_exec"}:
        allowed = set(errno_reasons.values())
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
        if message in pam_messages:
            pam_failures[pam_messages[message]] += 1
        for fixed in ("Minecraft manager listening", "minecraft-ui stopped", "Failed to start", "Stopped Minecraft Server UI manager"):
            if fixed in message:
                known_events[fixed] += 1
print(json.dumps({"priority_counts": dict(priorities), "known_event_counts": dict(known_events),
                  "native_update_failure_counts": dict(native_update_failures),
                  "native_schema_probe_counts": dict(schema_probe_failures),
                  "native_pam_failure_counts": dict(pam_failures)}))

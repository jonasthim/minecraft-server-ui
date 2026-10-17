#!/usr/bin/env python3
"""Reduce selected systemctl properties to finite updater context metadata."""
import json
import re
import sys

INPUT_LIMIT = 16 * 1024
PROPERTIES = {
    "Id", "User", "CapabilityBoundingSet", "PrivateUsers", "SystemCallFilter",
    "RestrictSUIDSGID", "NoNewPrivileges",
}


def unit_kind(value):
    if value == "minecraft-ui-update-recovery.service":
        return "recovery"
    if value == "minecraft-ui-qualification-capability-baseline.service":
        return "capability_probe_baseline"
    if value == "minecraft-ui-qualification-capability-control.service":
        return "capability_probe_control"
    prefix, suffix = "minecraft-ui-update@", ".service"
    if value.startswith(prefix) and value.endswith(suffix):
        instance = value[len(prefix):-len(suffix)]
        if 1 <= len(instance) <= 128 and re.fullmatch(r"[A-Za-z0-9_.\\x-]+", instance):
            return "updater_instance"
    return "unknown"


def unique(properties, duplicates, name):
    if name in duplicates or name not in properties:
        return None
    return properties[name]


def yes_no(value):
    return value if value in {"yes", "no"} else "unknown"


def summarize_unit(properties, duplicates):
    cap_set = unique(properties, duplicates, "CapabilityBoundingSet")
    syscall_filter = unique(properties, duplicates, "SystemCallFilter")
    if cap_set is None:
        cap_state, cap_setuid = "unknown", "unknown"
    elif not cap_set.strip():
        cap_state, cap_setuid = "empty", "no"
    elif cap_set.strip() == "~":
        cap_state, cap_setuid = "configured", "yes"
    else:
        cap_state = "configured"
        tokens = cap_set.split()
        if all(re.fullmatch(r"~?cap_[a-z0-9_]+", token, re.IGNORECASE) for token in tokens):
            inverted = tokens[0].startswith("~")
            valid_polarity = all(not token.startswith("~") for token in tokens[1:])
            if valid_polarity:
                named = any(token.lstrip("~").lower() == "cap_setuid" for token in tokens)
                cap_setuid = ("no" if named else "yes") if inverted else ("yes" if named else "no")
            else:
                cap_setuid = "unknown"
        else:
            cap_setuid = "unknown"
    if syscall_filter is None:
        syscall_state = syscall_mode = setuid_filter = "unknown"
    elif not syscall_filter.strip():
        syscall_state, syscall_mode, setuid_filter = "empty", "none", "no"
    elif syscall_filter.strip() == "~":
        syscall_state, syscall_mode, setuid_filter = "configured", "denylist", "no"
    else:
        syscall_state = "configured"
        tokens = syscall_filter.split()
        if all(re.fullmatch(r"~?(?:@[A-Za-z0-9_-]+|[a-z0-9_]+)", token) for token in tokens):
            inverted = tokens[0].startswith("~")
            syscall_mode = "denylist" if inverted else "allowlist"
            valid_polarity = all(not token.startswith("~") for token in tokens[1:])
            if valid_polarity:
                # Literal token presence only. Groups such as @system-service
                # may include syscalls without naming them here.
                named = any(token.lstrip("~").lower() in {"setuid", "@setuid"} for token in tokens)
                setuid_filter = "yes" if named else "no"
            else:
                syscall_mode = setuid_filter = "unknown"
        else:
            syscall_mode = setuid_filter = "unknown"
    user = unique(properties, duplicates, "User")
    user_state = "root" if user == "root" else "other" if user else "unknown"
    return {
        "unit": unit_kind(unique(properties, duplicates, "Id") or ""),
        "user": user_state,
        "capability_bounding_set": cap_state,
        "cap_setuid_named": cap_setuid,
        "private_users": yes_no(unique(properties, duplicates, "PrivateUsers")),
        "system_call_filter": syscall_state,
        "system_call_filter_mode": syscall_mode,
        "setuid_filter_named": setuid_filter,
        "restrict_suid_sgid": yes_no(unique(properties, duplicates, "RestrictSUIDSGID")),
        "no_new_privileges": yes_no(unique(properties, duplicates, "NoNewPrivileges")),
    }


def parse_blocks(raw):
    blocks = []
    properties = {}
    duplicates = set()

    def flush():
        nonlocal properties, duplicates
        if properties:
            blocks.append((properties, duplicates))
            properties, duplicates = {}, set()

    for line in raw.splitlines():
        if not line:
            flush()
            continue
        key, separator, value = line.partition("=")
        if not separator or key not in PROPERTIES:
            continue
        if key == "Id" and "Id" in properties:
            flush()
        if key in properties:
            duplicates.add(key)
        else:
            properties[key] = value
    flush()
    return blocks


def main():
    raw = sys.stdin.buffer.read(INPUT_LIMIT + 1)
    if len(raw) > INPUT_LIMIT:
        result = {"units": [], "error": "input_too_large"}
    else:
        try:
            text = raw.decode("utf-8")
        except UnicodeDecodeError:
            result = {"units": [], "error": "input_invalid"}
        else:
            result = {"units": [summarize_unit(properties, duplicates)
                                for properties, duplicates in parse_blocks(text)]}
    json.dump(result, sys.stdout, separators=(",", ":"))
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()

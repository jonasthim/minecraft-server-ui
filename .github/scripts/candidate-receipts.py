#!/usr/bin/env python3
"""Record commands run on this native CI host against its just-built binaries.

Receipts bind observations to hashes; they are not signatures or qualification.
This script must never be used to execute an untrusted downloaded candidate.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import subprocess


def require(condition, message):
    if not condition:
        raise ValueError(message)


def observe(path, command, version, target):
    require(path.is_file() and not path.is_symlink(), "Expected a built ordinary binary")
    path = path.resolve()
    output = subprocess.run([str(path), "version"], capture_output=True,
                            text=True, check=True, timeout=15).stdout.strip()
    require(output == f"{command} {version}", "Native version command mismatch")
    with path.open("rb") as binary:
        hasher = hashlib.sha256()
        for block in iter(lambda: binary.read(1 << 20), b""):
            hasher.update(block)
        digest = hasher.hexdigest()
    receipt = {"format": 1, "os": target, "arch": "amd64", "command": command,
               "version": version, "binary_sha256": digest}
    if command == "minecraft-ui":
        raw = subprocess.run([str(path), "update-capability"], capture_output=True,
                             text=True, check=True, timeout=15).stdout
        require(len(raw) <= 4096, "Capability response exceeds bound")
        capability = json.loads(raw)
        require(capability.get("format") == 1 and capability.get("version") == version,
                "Manager capability identity mismatch")
        schema = capability.get("schema")
        require(type(schema) is int and schema > 0, "Invalid native manager schema")
        receipt["schema"] = schema
    with open(str(path) + ".version.json", "x", encoding="utf-8") as out:
        json.dump(receipt, out, sort_keys=True)
        out.write("\n")
    print(f"Recorded native {target}/amd64 {command} {version}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True)
    parser.add_argument("--validate-only", action="store_true")
    parser.add_argument("--manager", type=Path)
    parser.add_argument("--updater", type=Path)
    parser.add_argument("--target", choices=("linux", "windows"))
    args = parser.parse_args()
    require(re.fullmatch(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", args.version),
            "Candidate version must have stable tag syntax")
    require(all(int(part) <= 2**64 - 1 for part in args.version[1:].split(".")),
            "Candidate version overflows")
    if args.validate_only:
        print("Candidate tag syntax valid; this does not authorize a stable release")
        return
    require(os.environ.get("GITHUB_ACTIONS") == "true", "Native CI receipt generation requires GitHub Actions")
    require(args.manager and args.updater and args.target, "Both native binaries and target are required")
    require(platform.system().lower() == args.target and platform.machine().lower() in ("amd64", "x86_64"),
            "Receipt generation requires the declared native x64 host")
    observe(args.manager, "minecraft-ui", args.version, args.target)
    observe(args.updater, "minecraft-ui-update", args.version, args.target)


if __name__ == "__main__":
    main()

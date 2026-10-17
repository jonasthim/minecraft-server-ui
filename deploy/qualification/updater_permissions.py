#!/usr/bin/env python3
"""Fixed-path, metadata-only Linux updater bootstrap diagnostics."""
import json
import os
from pathlib import Path
import stat
import sys


PATHS = (
    "/", "/usr", "/usr/local", "/usr/local/bin", "/usr/local/lib",
    "/usr/local/lib/minecraft-ui", "/usr/local/lib/minecraft-ui/selfupdate",
    "/usr/local/bin/minecraft-ui", "/var", "/var/lib",
    "/var/lib/minecraft-ui-update", "/var/lib/minecraft-ui-update/stages",
    "/etc", "/etc/minecraft-ui", "/etc/minecraft-ui/update-trust.json",
)
PHASES = {"before-prefix-provisioning", "after-prefix-provisioning", "before-cleanup", "after-prefix-restoration"}


def snapshot(phase):
    if phase not in PHASES:
        raise ValueError("fixed diagnostic phase required")
    records = []
    for path in PATHS:
        record = {"path": path}
        try:
            info = os.lstat(path)
            record.update(exists=True, uid=info.st_uid, gid=info.st_gid,
                          mode=format(stat.S_IMODE(info.st_mode), "o"),
                          directory=stat.S_ISDIR(info.st_mode), symlink=stat.S_ISLNK(info.st_mode))
        except FileNotFoundError:
            record["exists"] = False
        except OSError:
            record["metadata_read_failed"] = True
        records.append(record)
    return {"phase": phase, "paths": records}


def main():
    output = Path(sys.argv[1])
    history = json.loads(output.read_text()) if output.exists() else []
    history.append(snapshot(sys.argv[2]))
    output.write_text(json.dumps(history, indent=2))


if __name__ == "__main__":
    main()

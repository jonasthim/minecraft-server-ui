#!/usr/bin/env python3
"""Build the non-shipping v0.0.3 manager readiness-failure overlay."""

import argparse
import json
from pathlib import Path


ANCHOR = "\tdefer stop()\n"
INSERTION = '\tif len(os.Args) == 2 && os.Args[1] == "serve" {\n\t\t<-ctx.Done()\n\t\treturn\n\t}\n'


def make_overlay(source: Path, scratch: Path):
    if source.is_symlink() or not source.is_file():
        raise ValueError("ordinary manager source file required")
    source = source.resolve(strict=True)
    if scratch.exists() or scratch.is_symlink() or not scratch.parent.is_dir():
        raise ValueError("new private scratch directory required")
    original = source.read_text(encoding="utf-8")
    if original.count(ANCHOR) != 1:
        raise ValueError("expected exactly one signal context insertion point")
    if "import (\n" not in original or '"context"' not in original or '"os"' not in original or '"os/signal"' not in original:
        raise ValueError("manager command is missing required signal imports")
    modified = original.replace(ANCHOR, ANCHOR + INSERTION, 1)
    scratch.mkdir(mode=0o700)
    replacement = scratch / "main.go"
    replacement.write_text(modified, encoding="utf-8")
    overlay = scratch / "overlay.json"
    overlay.write_text(json.dumps({"Replace": {str(source): str(replacement)}}, separators=(",", ":")) + "\n", encoding="utf-8")
    return overlay, replacement


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--scratch", type=Path, required=True)
    args = parser.parse_args()
    try:
        overlay, _ = make_overlay(args.source, args.scratch)
    except (OSError, ValueError) as error:
        parser.error(str(error))
    print(overlay)


if __name__ == "__main__":
    main()

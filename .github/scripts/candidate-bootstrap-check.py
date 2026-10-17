#!/usr/bin/env python3
"""Exercise this workflow's native preparer; never install or run bundle members.

The workflow supplies same-commit build artifacts. Its unsigned entry receipt
is provenance, not an independent public-download trust anchor.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import stat
import subprocess
import tempfile


def require(value):
    if not value:
        raise ValueError("native bootstrap assertion failed")


def read_json(path, limit):
    require(path.is_file() and not path.is_symlink())
    with path.open("rb") as source:
        body = source.read(limit + 1)
    require(0 < len(body) <= limit)
    return json.loads(body)


def digest(path):
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def output_snapshot(directory):
    """Bind the entire prepared output, including identity, without logging it."""
    def metadata(path, expected_type):
        value = path.lstat()
        require(expected_type(value.st_mode))
        return (value.st_dev, value.st_ino, value.st_mode, value.st_size,
                value.st_mtime_ns, value.st_ctime_ns)

    snapshot = {".": metadata(directory, stat.S_ISDIR)}
    for path in directory.iterdir():
        before = metadata(path, stat.S_ISREG)
        checksum = digest(path)
        require(metadata(path, stat.S_ISREG) == before)
        snapshot[path.name] = (before, checksum)
    require(metadata(directory, stat.S_ISDIR) == snapshot["."])
    return snapshot


def run_preparer(command, scratch, label):
    stdout = scratch / (label + ".out")
    stderr = scratch / (label + ".err")
    with stdout.open("xb") as out, stderr.open("xb") as err:
        result = subprocess.run(command, stdin=subprocess.DEVNULL, stdout=out,
                                stderr=err, timeout=660, check=False)
    require(stdout.stat().st_size <= 32768 and stderr.stat().st_size <= 32768)
    return result.returncode, stdout


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True)
    parser.add_argument("--target", required=True, choices=("linux", "windows"))
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--entry-directory", required=True, type=Path)
    parser.add_argument("--artifacts", required=True, type=Path)
    parser.add_argument("--report", required=True, type=Path)
    args = parser.parse_args()
    require(os.environ.get("GITHUB_ACTIONS") == "true")
    require(os.environ.get("RUNNER_ENVIRONMENT") == "github-hosted")
    require(platform.system().lower() == args.target)
    require(platform.machine().lower() in ("amd64", "x86_64"))
    require(re.fullmatch(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", args.version))
    require(re.fullmatch(r"[0-9a-f]{40}", os.environ.get("GITHUB_SHA", "")))
    require(not args.report.exists() and not args.report.is_symlink())
    result = {"scope": "native-offline-development-bootstrap-only", "failed": True,
              "source_commit": os.environ["GITHUB_SHA"], "os": args.target,
              "version": args.version, "stage": "input_binding"}
    try:
        require(args.binary.is_file() and not args.binary.is_symlink())
        binary = args.binary.resolve()
        artifacts = args.artifacts.resolve(strict=True)
        entry = read_json(args.entry_directory / "bootstrap-entry.json", 32768)
        require(entry["repository"] == "jonasthim/minecraft-server-ui")
        require(entry["format"] == 1 and entry["preparer_version"] == args.version and entry["development"] is True)
        require(entry["trust_file_sha256"] == digest(artifacts / "development-trust.json"))
        expected = next(asset for asset in entry["assets"] if asset["name"] == binary.name)
        require(binary.stat().st_size == expected["size"] and digest(binary) == expected["sha256"])
        result["preparer_sha256"] = expected["sha256"]
        if args.target == "linux":
            binary.chmod(0o755)  # Actions artifact transport does not retain mode.
        with tempfile.TemporaryDirectory(prefix="native-bootstrap-", dir=os.environ["RUNNER_TEMP"]) as temp:
            scratch = Path(temp).resolve()
            output = scratch / "prepared"
            command = [str(binary), "prepare", "--repository", "jonasthim/minecraft-server-ui",
                       "--version", args.version, "--trust-file", str(artifacts / "development-trust.json"),
                       "--artifacts", str(artifacts), "--output", str(output)]
            result["stage"] = "development_refusal"
            code, stdout = run_preparer(command, scratch, "refused")
            require(code != 0 and stdout.stat().st_size == 0 and not output.exists())
            result["development_default_refused"] = True
            result["stage"] = "prepare"
            code, stdout = run_preparer(command + ["--allow-development"], scratch, "prepared")
            require(code == 0)
            receipt = read_json(output / "completion.json", 32768)
            require(receipt == read_json(stdout, 32768))
            manifest = read_json(artifacts / "bootstrap.json", 32768)
            artifact = next(item for item in manifest["artifacts"] if item["os"] == args.target)
            require(receipt["repository"] == "jonasthim/minecraft-server-ui")
            require(receipt["version"] == args.version and receipt["schema"] == manifest["schema"])
            require(receipt["os"] == args.target and receipt["arch"] == "amd64")
            require(receipt["trust"] == "DEVELOPMENT ONLY")
            require(receipt["manifest_sha256"] == digest(artifacts / "bootstrap.json"))
            require(receipt["members"] == artifact["members"])
            result["stage"] = "extracted_members"
            members = artifact["members"]
            require(len(members) == (14 if args.target == "linux" else 5))
            require({path.name for path in output.iterdir()} == {member["name"] for member in members} | {"completion.json"})
            for member in members:
                require(member["name"] == Path(member["name"]).name)
                path = output / member["name"]
                require(path.is_file() and not path.is_symlink())
                require(path.stat().st_size == member["size"] and digest(path) == member["sha256"])
                if args.target == "linux":
                    require(path.stat().st_mode & 0o777 == member["mode"])
            result["member_count"] = len(members)
            result["member_hashes_verified"] = True
            result["stage"] = "existing_output_refusal"
            before = output_snapshot(output)
            code, stdout = run_preparer(command + ["--allow-development"], scratch, "existing")
            require(code != 0 and stdout.stat().st_size == 0)
            require(output_snapshot(output) == before)
            result["existing_output_preserved"] = True
            result["completion_receipt_sha256"] = before["completion.json"][1]
        result["stage"] = "complete"
        result["failed"] = False
    except Exception:
        # No subprocess output, exception text, private path or artifact content.
        print("FAIL native bootstrap stage=" + result["stage"])
    finally:
        with args.report.open("x", encoding="utf-8") as report:
            json.dump(result, report, sort_keys=True)
            report.write("\n")
    if result["failed"]:
        raise SystemExit(1)
    print("PASS native development bootstrap preparation; no installed qualification")


if __name__ == "__main__":
    main()

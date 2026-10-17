#!/usr/bin/env bash
# Task 32B diagnostic only. Reproduces the shipped updater sandbox with a
# harmless fixed probe and a control unit whose bounding set is exactly the two
# capabilities used by its fixed child transition (CAP_SETUID/CAP_SETGID).
set -euo pipefail
umask 077

write_probe() {
    cat >"$1" <<'PY'
#!/usr/bin/python3
import ctypes
import errno
import json
import os
import pwd
import re
import shutil
import stat
import subprocess
import sys
import time

PREFIX = "native-capability-probe:v1:"
CAP_BITS = {"setgid": 6, "setuid": 7}
SECUREBITS = {
    "noroot": 1 << 0, "noroot_locked": 1 << 1,
    "no_setuid_fixup": 1 << 2, "no_setuid_fixup_locked": 1 << 3,
    "keep_caps": 1 << 4, "keep_caps_locked": 1 << 5,
    "no_cap_ambient_raise": 1 << 6, "no_cap_ambient_raise_locked": 1 << 7,
}
ERRNO_NAMES = {errno.EPERM: "eperm", errno.EACCES: "eacces", errno.EINVAL: "einval",
               errno.ENOSYS: "enosys", errno.EOPNOTSUPP: "unsupported"}


def proc_snapshot():
    try:
        with open("/proc/thread-self/status", "r", encoding="ascii", errors="strict") as stream:
            return parse_status(stream.read(16 * 1024 + 1))
    except (OSError, UnicodeError):
        return None


def parse_status(raw):
    if len(raw) > 16 * 1024:
        return None
    values = {}
    for line in raw.splitlines():
        key, sep, value = line.partition(":")
        if sep and key in {"Uid", "Gid", "CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb", "NoNewPrivs", "Seccomp"}:
            if key in values:
                return None
            values[key] = value.strip()
    try:
        uid_text, gid_text = values["Uid"].split(), values["Gid"].split()
        if any(not re.fullmatch(r"[0-9]+", item) for item in uid_text + gid_text):
            return None
        uid = [int(item, 10) for item in uid_text]
        gid = [int(item, 10) for item in gid_text]
        if len(uid) != 4 or len(gid) != 4 or any(v > 0xFFFFFFFF for v in uid + gid):
            return None
        masks = {}
        for key in ("CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb"):
            if not re.fullmatch(r"[0-9A-Fa-f]{1,16}", values[key]):
                return None
            masks[key] = int(values[key], 16)
        nnp = int(values["NoNewPrivs"], 10)
        seccomp = int(values["Seccomp"], 10)
        if nnp not in (0, 1) or seccomp not in (0, 1, 2):
            return None
    except (KeyError, ValueError):
        return None
    cap = {}
    for cap_name, bit in CAP_BITS.items():
        for label, key in (("inheritable", "CapInh"), ("permitted", "CapPrm"),
                           ("effective", "CapEff"), ("bounding", "CapBnd"),
                           ("ambient", "CapAmb")):
            cap[f"{cap_name}_{label}"] = "yes" if masks[key] & (1 << bit) else "no"
    uid_map = "unknown"
    try:
        with open("/proc/thread-self/uid_map", "r", encoding="ascii", errors="strict") as stream:
            map_raw = stream.read(257)
        uid_map = classify_uid_map(map_raw)
    except (OSError, UnicodeError):
        pass
    secure = {name: "unknown" for name in SECUREBITS}
    try:
        libc = ctypes.CDLL(None, use_errno=True)
        libc.prctl.restype = ctypes.c_int
        bits = libc.prctl(27, 0, 0, 0, 0)  # PR_GET_SECUREBITS, read-only.
        if bits >= 0:
            secure = {name: "yes" if bits & mask else "no" for name, mask in SECUREBITS.items()}
    except (AttributeError, OSError):
        pass
    return {
        "euid_root": "yes" if uid[1] == 0 else "no",
        "egid_root": "yes" if gid[1] == 0 else "no",
        "uid_map": uid_map,
        "capabilities": cap,
        "no_new_privileges": "yes" if nnp else "no",
        "seccomp": {0: "disabled", 1: "strict", 2: "filter"}[seccomp],
        "securebits": secure,
        "python_file": capability_file(__file__),
        "interpreter_file": capability_file(sys.executable),
        "runuser_file": capability_file(shutil.which("runuser")) if shutil.which("runuser") else {
            "metadata": "unavailable", "owner_root": "unknown", "setuid_mode": "unknown",
            "setgid_mode": "unknown", "file_capabilities": "unavailable"},
        "transitions": {"uid_only": "unknown", "runuser_sequence": "unknown"},
    }


def classify_uid_map(raw):
    if len(raw) > 256:
        return "unknown"
    lines = [line.strip() for line in raw.splitlines()]
    if len(lines) != 1:
        return "unknown"
    match = re.fullmatch(r"([0-9]+)\s+([0-9]+)\s+([0-9]+)", lines[0])
    if not match:
        return "unknown"
    inside, outside, length = (int(item, 10) for item in match.groups())
    if (length == 0 or inside > 0xFFFFFFFF or outside > 0xFFFFFFFF
            or inside + length > 0x100000000 or outside + length > 0x100000000):
        return "unknown"
    return "initial" if inside == 0 and outside == 0 and length in {4294967295, 4294967296} else "other"


def capability_file(path):
    unknown = {"metadata": "unknown", "owner_root": "unknown", "setuid_mode": "unknown",
               "setgid_mode": "unknown", "file_capabilities": "unavailable"}
    try:
        info = os.stat(path)
        if not stat.S_ISREG(info.st_mode):
            unknown["metadata"] = "not_regular"
            return unknown
        result = {"metadata": "regular", "owner_root": "yes" if info.st_uid == 0 else "no",
                  "setuid_mode": "yes" if info.st_mode & stat.S_ISUID else "no",
                  "setgid_mode": "yes" if info.st_mode & stat.S_ISGID else "no",
                  "file_capabilities": "unknown"}
    except OSError:
        return unknown
    getcap = shutil.which("getcap")
    if not getcap:
        return result
    try:
        probe = subprocess.run([getcap, "-n", path], capture_output=True, text=True,
                               timeout=3, check=False)
    except (OSError, subprocess.TimeoutExpired):
        return result
    if probe.returncode != 0:
        return result
    output = probe.stdout.strip()
    if not output:
        result["file_capabilities"] = "none"
        return result
    prefix = path + " "
    capabilities = output[len(prefix):].strip() if output.startswith(prefix) else ""
    names = re.findall(r"cap_([a-z0-9_]+)", capabilities.lower()) if capabilities else []
    if not names or not re.fullmatch(r"[a-z0-9_,=+ep-]+", capabilities.lower()):
        return result
    has_uid, has_gid = "setuid" in names, "setgid" in names
    if has_uid and has_gid:
        result["file_capabilities"] = "setuid_setgid"
    elif has_uid:
        result["file_capabilities"] = "setuid"
    elif has_gid:
        result["file_capabilities"] = "setgid"
    else:
        result["file_capabilities"] = "other_present"
    return result


def errno_category(number):
    return ERRNO_NAMES.get(number, "other")


def exec_with_report(write_fd, exec_function):
    try:
        exec_function()
    except OSError as error:
        payload = f"exec_{errno_category(error.errno)}".encode("ascii")[:64]
        try:
            os.write(write_fd, payload)
        except OSError:
            pass
        return 127
    except BaseException:
        try:
            os.write(write_fd, b"child_other")
        except OSError:
            pass
        return 127
    return 0


def run_child_transition(kind):
    try:
        nobody = pwd.getpwnam("nobody")
    except (KeyError, OSError):
        return "target_unavailable"
    if nobody.pw_uid <= 0 or nobody.pw_gid <= 0 or nobody.pw_uid > 0xFFFFFFFF or nobody.pw_gid > 0xFFFFFFFF:
        return "target_invalid"
    read_fd, write_fd = os.pipe()
    try:
        pid = os.fork()
    except OSError as error:
        os.close(read_fd)
        os.close(write_fd)
        return "fork_" + errno_category(error.errno)
    if pid == 0:
        try:
            os.close(read_fd)
            null_fd = os.open(os.devnull, os.O_WRONLY)
            os.dup2(null_fd, 1)
            os.dup2(null_fd, 2)
            if kind == "runuser_sequence":
                steps = (("setgroups", lambda: os.setgroups([])),
                         ("setgid", lambda: os.setgid(nobody.pw_gid)))
            else:
                steps = ()
            steps += (("setuid", lambda: os.setuid(nobody.pw_uid)),)
            for stage, operation in steps:
                try:
                    operation()
                except OSError as error:
                    payload = f"{stage}_{errno_category(error.errno)}".encode("ascii")
                    os.write(write_fd, payload[:64])
                    os._exit(0)
            if os.geteuid() != nobody.pw_uid:
                os.write(write_fd, b"verify_failed")
                os._exit(0)
            # os.pipe descriptors are close-on-exec by default, but remain open
            # long enough to report an exec failure before the child exits.
            exit_code = exec_with_report(
                write_fd,
                lambda: os.execve("/usr/bin/id", ["id", "-u"], {"PATH": "/usr/bin:/bin"}),
            )
            os._exit(exit_code)
        except BaseException:
            os._exit(127)
    os.close(write_fd)
    os.set_blocking(read_fd, False)
    payload = b""
    pipe_eof = False
    deadline = time.monotonic() + 3.0
    status = None

    def collect_payload():
        nonlocal payload, pipe_eof
        while not pipe_eof:
            try:
                chunk = os.read(read_fd, 64)
            except BlockingIOError:
                return
            except OSError:
                pipe_eof = True
                return
            if not chunk:
                pipe_eof = True
                return
            payload += chunk

    try:
        while time.monotonic() < deadline:
            collect_payload()
            try:
                waited, child_status = os.waitpid(pid, os.WNOHANG)
            except OSError:
                return "wait_other"
            if waited == pid:
                collect_payload()
                status = child_status
                break
            # EOF only means exec closed the non-inheritable write end.
            # Completion is determined by waitpid, so an exiting child cannot
            # be mistaken for a still-running child.
            time.sleep(0.01)
        if status is None:
            try:
                os.kill(pid, 9)
            except OSError:
                pass
            kill_deadline = time.monotonic() + 1.0
            while time.monotonic() < kill_deadline:
                try:
                    waited, child_status = os.waitpid(pid, os.WNOHANG)
                except OSError:
                    break
                if waited == pid:
                    status = child_status
                    break
                time.sleep(0.01)
            return "timeout"
    finally:
        try:
            os.close(read_fd)
        except OSError:
            pass
    if payload:
        value = payload.decode("ascii", "ignore")
        if re.fullmatch(r"(?:setgroups|setgid|setuid|exec)_(?:eperm|eacces|einval|enosys|unsupported|other)|verify_failed|child_other", value):
            return value
        return "child_other"
    if status is not None and os.WIFSIGNALED(status):
        return "child_signaled"
    return "success" if status is not None and os.WIFEXITED(status) and os.WEXITSTATUS(status) == 0 else "child_exit_other"


def emit(stage):
    context = proc_snapshot()
    if context is None:
        context = {"status": "unknown"}
    else:
        context["transitions"] = {"uid_only": run_child_transition("uid_only"),
                                  "runuser_sequence": run_child_transition("runuser_sequence")}
    print(PREFIX + json.dumps({"stage": stage, "context": context}, separators=(",", ":")), flush=True)


def summarize():
    raw = sys.stdin.buffer.read(32 * 1024 + 1)
    if len(raw) > 32 * 1024:
        print('{"marker_status":"input_too_large","records":[]}')
        return 1
    records = []
    invalid = False
    try:
        lines = raw.decode("utf-8").splitlines()
    except UnicodeDecodeError:
        lines, invalid = [], True
    for line in lines:
        if not line.startswith(PREFIX):
            continue
        try:
            value = json.loads(line[len(PREFIX):])
            if set(value) != {"stage", "context"} or value["stage"] not in {"execstartpre", "execstart"}:
                invalid = True
                continue
            if not isinstance(value["context"], dict):
                invalid = True
                continue
            context = value["context"]
            if not valid_context(context):
                invalid = True
                continue
            records.append({"stage": value["stage"], "context": context})
        except (ValueError, TypeError):
            invalid = True
    stages = [item["stage"] for item in records]
    if invalid or len(records) > 2 or len(stages) != len(set(stages)):
        status = "invalid"
        records = []
    elif set(stages) == {"execstartpre", "execstart"}:
        status = "complete"
    elif records:
        status = "partial"
    else:
        status = "missing"
    print(json.dumps({"marker_status": status, "records": records}, separators=(",", ":")))
    return 0


def valid_context(value):
    enum_values = {"yes", "no", "unknown"}
    secure_names = set(SECUREBITS)
    cap_names = {f"{cap}_{kind}" for cap in CAP_BITS for kind in
                 ("inheritable", "permitted", "effective", "bounding", "ambient")}
    file_names = {"metadata", "owner_root", "setuid_mode", "setgid_mode", "file_capabilities"}
    file_categories = {"none", "setuid", "setgid", "setuid_setgid", "other_present", "unknown", "unavailable"}
    if value == {"status": "unknown"}:
        return True
    if set(value) != {"euid_root", "egid_root", "uid_map", "capabilities", "no_new_privileges",
                      "seccomp", "securebits", "python_file", "interpreter_file", "runuser_file", "transitions"}:
        return False
    if any(value[key] not in enum_values for key in ("euid_root", "egid_root", "no_new_privileges")):
        return False
    if value["uid_map"] not in {"initial", "other", "unknown"} or value["seccomp"] not in {"disabled", "strict", "filter", "unknown"}:
        return False
    if not isinstance(value["capabilities"], dict) or set(value["capabilities"]) != cap_names:
        return False
    if any(item not in enum_values for item in value["capabilities"].values()):
        return False
    if not isinstance(value["securebits"], dict) or set(value["securebits"]) != secure_names:
        return False
    if any(item not in enum_values for item in value["securebits"].values()):
        return False
    for field in ("python_file", "interpreter_file", "runuser_file"):
        item = value[field]
        if not isinstance(item, dict) or set(item) != file_names:
            return False
        if item["metadata"] not in {"regular", "unknown", "not_regular", "unavailable"}:
            return False
        if item["owner_root"] not in enum_values or item["setuid_mode"] not in enum_values or item["setgid_mode"] not in enum_values:
            return False
        if item["file_capabilities"] not in file_categories:
            return False
    transitions = value["transitions"]
    transition_categories = {"success", "target_unavailable", "target_invalid", "timeout", "verify_failed",
                             "child_other", "child_exit_other", "child_signaled", "wait_other"}
    for stage in ("setgroups", "setgid", "setuid", "exec", "fork"):
        transition_categories.update(f"{stage}_{error}" for error in
                                     ("eperm", "eacces", "einval", "enosys", "unsupported", "other"))
    if not isinstance(transitions, dict) or set(transitions) != {"uid_only", "runuser_sequence"}:
        return False
    return all(item in transition_categories for item in transitions.values())


def self_test():
    assert errno_category(errno.EPERM) == "eperm"
    assert errno_category(9999) == "other"
    read_fd, write_fd = os.pipe()
    try:
        def failed_exec():
            raise OSError(errno.ENOENT, "fixture")
        assert exec_with_report(write_fd, failed_exec) == 127
        os.close(write_fd)
        write_fd = -1
        assert os.read(read_fd, 64) == b"exec_other"
    finally:
        os.close(read_fd)
        if write_fd >= 0:
            os.close(write_fd)
    assert SECUREBITS["noroot"] == 1 and SECUREBITS["keep_caps_locked"] == 1 << 5
    sample = ("Uid:\t0\t0\t0\t0\nGid:\t0\t0\t0\t0\n"
              "CapInh:\t0000000000000000\nCapPrm:\t00000000000000c0\n"
              "CapEff:\t00000000000000c0\nCapBnd:\t00000000000000c0\n"
              "CapAmb:\t0000000000000000\nNoNewPrivs:\t1\nSeccomp:\t2\n")
    parsed = parse_status(sample)
    assert parsed is not None
    assert parsed["euid_root"] == "yes" and parsed["egid_root"] == "yes"
    assert parsed["capabilities"]["setuid_effective"] == "yes"
    assert parsed["capabilities"]["setgid_permitted"] == "yes"
    assert parsed["no_new_privileges"] == "yes" and parsed["seccomp"] == "filter"
    assert parse_status(sample + "Uid:\t0\t0\t0\t0\n") is None
    assert parse_status(sample.replace("Uid:\t0\t0\t0\t0", "Uid:\t+0\t0\t0\t0")) is None
    assert parse_status(sample.replace("Gid:\t0\t0\t0\t0", "Gid:\t4294967296\t0\t0\t0")) is None
    assert parse_status(sample.replace("CapEff:\t00000000000000c0", "CapEff:\t10000000000000000")) is None
    assert classify_uid_map("         0          0 4294967295\n") == "initial"
    assert classify_uid_map("0 100000 1\n") == "other"
    assert classify_uid_map("0 0 0\n") == "unknown"
    assert classify_uid_map("4294967295 0 2\n") == "unknown"
    assert classify_uid_map("999999999999999999999999 0 1\n") == "unknown"
    assert valid_context({"status": "unknown"})
    assert not valid_context({"status": "unknown", "raw": "must-not-pass"})
    print("linux updater context pure fixtures passed")


if __name__ == "__main__":
    if len(sys.argv) == 2 and sys.argv[1] == "--self-test":
        self_test()
    elif len(sys.argv) == 2 and sys.argv[1] == "summarize":
        raise SystemExit(summarize())
    elif len(sys.argv) == 3 and sys.argv[1] == "file-metadata":
        print(json.dumps(capability_file(sys.argv[2]), separators=(",", ":")))
    elif len(sys.argv) == 2 and sys.argv[1] in {"execstartpre", "execstart"}:
        emit(sys.argv[1])
    else:
        raise SystemExit(2)
PY
}

unit_preflight_absent() {
    local name=$1 command=${2:-systemctl} installed loaded
    if ! installed=$("$command" list-unit-files --no-legend --no-pager "$name" 2>/dev/null); then
        return 2
    fi
    if ! loaded=$("$command" list-units --all --no-legend --no-pager "$name" 2>/dev/null); then
        return 2
    fi
    [[ -z $installed && -z $loaded ]]
}

loaded_probe_uses_owned_fragment() {
    local name=$1 fragment
    fragment=$(systemctl show --value --property=FragmentPath "$name" 2>/dev/null) || return 1
    [[ $fragment == "$unit_dir/$name" ]]
}

stop_owned_probe_units() {
    local name loaded
    for name in "$baseline" "$control"; do
        loaded=$(systemctl list-units --all --no-legend --no-pager "$name" 2>/dev/null) || return 1
        if [[ -n $loaded ]]; then
            loaded_probe_uses_owned_fragment "$name" || return 1
            timeout 20s systemctl stop "$name" >/dev/null 2>&1 || return 1
        fi
    done
}

no_probe_unit_loaded() {
    local name loaded
    for name in "$baseline" "$control"; do
        loaded=$(systemctl list-units --all --no-legend --no-pager "$name" 2>/dev/null) || return 1
        [[ -z $loaded ]] || return 1
    done
}

remove_owned_unit_files() {
    local name path expected actual owned_flag
    for name in "$baseline" "$control"; do
        if [[ $name == "$baseline" ]]; then
            owned_flag=$baseline_file_owned
            expected=$baseline_file_id
        else
            owned_flag=$control_file_owned
            expected=$control_file_id
        fi
        [[ $owned_flag == 1 ]] || continue
        path=$unit_dir/$name
        actual=$(stat -c '%d:%i:%u:%g' -- "$path" 2>/dev/null) || return 1
        [[ $actual == "$expected" && ! -L $path ]] || return 1
        rm -- "$path" || return 1
    done
}

remove_owned_helper() {
    local actual
    [[ $owned == 1 ]] || return 0
    actual=$(stat -c '%d:%i:%u:%g' -- "$helper" 2>/dev/null) || return 1
    [[ $actual == "$helper_id" && ! -L $helper ]] || return 1
    rm -- "$helper"
}

create_unit_file() {
    local name=$1 path=$unit_dir/$1 identity
    if ! (set -o noclobber; : >"$path"); then
        return 1
    fi
    if [[ $name == "$baseline" ]]; then
        baseline_file_owned=1
        baseline_file_id=unknown
    else
        control_file_owned=1
        control_file_id=unknown
    fi
    identity=$(stat -c '%d:%i:%u:%g' -- "$path") || return 1
    if [[ $name == "$baseline" ]]; then
        baseline_file_owned=1
        baseline_file_id=$identity
    else
        control_file_owned=1
        control_file_id=$identity
    fi
}

pure_preflight_test() {
    fixture_installed=
    fixture_loaded=
    fixture_files_exit=0
    fixture_units_exit=0
    fixture_systemctl() {
        case $1 in
            list-unit-files)
                [[ $fixture_files_exit == 0 ]] || return 1
                printf '%s' "$fixture_installed"
                ;;
            list-units)
                [[ $fixture_units_exit == 0 ]] || return 1
                printf '%s' "$fixture_loaded"
                ;;
            *) return 1 ;;
        esac
    }
    unit_preflight_absent fixed.service fixture_systemctl || return 1
    fixture_installed='fixed.service enabled enabled'
    if unit_preflight_absent fixed.service fixture_systemctl; then return 1; fi
    fixture_installed=
    fixture_loaded='fixed.service loaded inactive dead'
    if unit_preflight_absent fixed.service fixture_systemctl; then return 1; fi
    fixture_loaded=
    fixture_files_exit=1
    if unit_preflight_absent fixed.service fixture_systemctl; then return 1; fi
    fixture_files_exit=0
    fixture_units_exit=1
    if unit_preflight_absent fixed.service fixture_systemctl; then return 1; fi
}

if [[ ${1:-} == --pure-fixture && $# == 1 ]]; then
    fixture_dir=$(mktemp -d)
    trap 'rm -rf -- "$fixture_dir"' EXIT
    write_probe "$fixture_dir/probe.py"
    chmod 0500 "$fixture_dir/probe.py"
    python3 "$fixture_dir/probe.py" --self-test
    pure_preflight_test || { echo 'unit preflight fixtures failed' >&2; exit 1; }
    echo 'linux updater unit preflight pure fixtures passed'
    exit 0
fi

[[ ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted && $EUID == 0 ]] || { echo 'Disposable hosted runner/root required.' >&2; exit 2; }
[[ $# == 2 && -n ${GITHUB_WORKSPACE:-} ]] || { echo 'Expected the updater binary and empty hosted report directory.' >&2; exit 2; }
[[ $(. /etc/os-release; printf '%s' "${ID:-}:${VERSION_ID:-}") == 'ubuntu:24.04' ]] || { echo 'Ubuntu 24.04 hosted runner required.' >&2; exit 2; }
[[ -f $1 && ! -L $1 ]] || { echo 'Ordinary updater binary required.' >&2; exit 2; }
updater_binary=$(realpath -e -- "$1")
output=$(realpath -e -- "$2")
workspace=$(realpath -e -- "$GITHUB_WORKSPACE")
[[ "$updater_binary" == "$workspace/"* && -f $updater_binary && ! -L $updater_binary ]] || { echo 'Updater binary must be an ordinary workspace file.' >&2; exit 2; }
[[ "$output" == "$workspace/"* && -d $output && ! -L $output ]] || exit 2
[[ -z $(find "$output" -mindepth 1 -maxdepth 1 -print -quit) ]] || { echo 'Report directory must be empty.' >&2; exit 2; }
[[ ! -e /var/lib/minecraft-ui && ! -L /var/lib/minecraft-ui && ! -e /var/lib/minecraft-ui-update && ! -L /var/lib/minecraft-ui-update && ! -e /usr/local/bin/minecraft-ui && ! -L /usr/local/bin/minecraft-ui ]] || { echo 'Existing Minecraft installation present.' >&2; exit 2; }
! id minecraft-ui &>/dev/null || { echo 'Existing manager account present.' >&2; exit 2; }
! getent group minecraft-ui &>/dev/null || { echo 'Existing manager group present.' >&2; exit 2; }

helper=/run/minecraft-ui-qualification-capability-probe.py
unit_dir=/run/systemd/system
baseline=minecraft-ui-qualification-capability-baseline.service
control=minecraft-ui-qualification-capability-control.service
[[ -d $unit_dir && ! -L $unit_dir && ! -e $helper && ! -L $helper ]] || { echo 'Fixed probe path already exists.' >&2; exit 2; }
for name in "$baseline" "$control"; do
    [[ ! -e $unit_dir/$name && ! -L $unit_dir/$name ]] || { echo 'Fixed probe unit path already exists.' >&2; exit 2; }
    unit_preflight_absent "$name" || { echo 'Fixed probe unit is installed/loaded or systemd preflight failed.' >&2; exit 2; }
done
getent passwd nobody >/dev/null || { echo 'Existing nobody account required.' >&2; exit 2; }

scratch=$(mktemp -d /run/minecraft-ui-capability-probe.XXXXXX)
owned=0
units_owned=0
helper_id=
baseline_file_owned=0
baseline_file_id=
control_file_owned=0
control_file_id=
state_update_id=
state_manager_id=
cleanup() {
    status=$?
    trap - EXIT
    set +e
    if [[ $units_owned == 1 ]]; then
        if stop_owned_probe_units; then
            remove_owned_unit_files || status=1
            remove_owned_helper || status=1
            systemctl daemon-reload || status=1
            systemctl reset-failed "$baseline" "$control" >/dev/null 2>&1 || true
        else
            status=1
        fi
        [[ ! -e $unit_dir/$baseline && ! -e $unit_dir/$control && ! -e $helper ]] || status=1
    elif [[ $baseline_file_owned == 1 || $control_file_owned == 1 ]]; then
        if no_probe_unit_loaded; then
            remove_owned_unit_files || status=1
            remove_owned_helper || status=1
        else
            status=1
        fi
    elif [[ $owned == 1 ]]; then
        remove_owned_helper || status=1
        [[ ! -e $helper ]] || status=1
    fi
    if [[ -n $state_update_id ]]; then
        [[ $(stat -c '%d:%i:%u:%g' /var/lib/minecraft-ui-update 2>/dev/null) == "$state_update_id" ]] && rmdir /var/lib/minecraft-ui-update || status=1
    fi
    if [[ -n $state_manager_id ]]; then
        [[ $(stat -c '%d:%i:%u:%g' /var/lib/minecraft-ui 2>/dev/null) == "$state_manager_id" ]] && rmdir /var/lib/minecraft-ui || status=1
    fi
    rm -rf -- "$scratch"
    printf '{"exit_code":%s,"scope":"linux-updater-capability-context-probe","cleanup_checked":true}\n' "$status" >"$output/result.json"
    chmod 0644 "$output/result.json" "$output/updater-capability-probe-summary.json" 2>/dev/null || true
    exit "$status"
}
trap cleanup EXIT

if ! (set -o noclobber; : >"$helper"); then
    echo 'Could not exclusively create fixed probe helper.' >&2
    exit 2
fi
owned=1
helper_id=$(stat -c '%d:%i:%u:%g' -- "$helper")
write_probe "$helper"
chmod 0500 "$helper"
mkdir -m 0700 /var/lib/minecraft-ui-update
state_update_id=$(stat -c '%d:%i:%u:%g' /var/lib/minecraft-ui-update)
mkdir -m 0700 /var/lib/minecraft-ui
state_manager_id=$(stat -c '%d:%i:%u:%g' /var/lib/minecraft-ui)
write_unit() {
    local name=$1 variant=$2 path=$unit_dir/$1 expected actual
    if [[ $name == "$baseline" ]]; then expected=$baseline_file_id; else expected=$control_file_id; fi
    actual=$(stat -c '%d:%i:%u:%g' -- "$path") || return 1
    [[ $actual == "$expected" && ! -L $path ]] || return 1
    cat >>"$path" <<UNIT
[Unit]
Description=Task32B disposable updater capability probe ($variant)
[Service]
Type=oneshot
User=root
Group=root
ExecStartPre=/usr/bin/python3 $helper execstartpre
ExecStart=/usr/bin/python3 $helper execstart
TimeoutStartSec=45s
UMask=0077
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/usr/local/bin /var/lib/minecraft-ui-update /var/lib/minecraft-ui
PrivateTmp=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
RestrictSUIDSGID=true
NoNewPrivileges=true
StandardOutput=journal
StandardError=journal
UNIT
}
create_unit_file "$baseline" || { echo 'Could not exclusively create baseline unit file.' >&2; exit 2; }
create_unit_file "$control" || { echo 'Could not exclusively create control unit file.' >&2; exit 2; }
units_owned=1
write_unit "$baseline" baseline
write_unit "$control" control
    cat >>"$unit_dir/$control" <<'UNIT'
CapabilityBoundingSet=CAP_SETUID CAP_SETGID
UNIT
systemctl daemon-reload

run_probe_unit() {
    local name=$1 label=$2 start_status=success
    if timeout 60s systemctl start "$name" >/dev/null 2>&1; then
        start_status=success
    else
        code=$?
        if [[ $code == 124 ]]; then start_status=timeout; else start_status=failed; fi
    fi
    systemctl show "$name" -p Id -p User -p CapabilityBoundingSet -p PrivateUsers -p SystemCallFilter -p RestrictSUIDSGID -p NoNewPrivileges >"$scratch/$label.properties.raw" 2>/dev/null || true
    python3 "$here/summarize-updater-unit.py" <"$scratch/$label.properties.raw" >"$scratch/$label.properties.json"
    journalctl -b -u "$name" --no-pager -o cat >"$scratch/$label.journal.raw" 2>/dev/null || true
    python3 "$helper" summarize <"$scratch/$label.journal.raw" >"$scratch/$label.markers.json"
    printf '%s\n' "$start_status" >"$scratch/$label.start"
}
here=$(cd -- "$(dirname -- "$0")" && pwd)
python3 "$helper" file-metadata "$updater_binary" >"$scratch/updater-file.json"
run_probe_unit "$baseline" baseline
run_probe_unit "$control" control

python3 - "$scratch" "$output/updater-capability-probe-summary.json" <<'PY'
import json, os, sys
scratch, destination = sys.argv[1:]
result = {"probe": "updater_capability_context_v1", "cases": []}
with open(os.path.join(scratch, "updater-file.json"), "r", encoding="utf-8") as stream:
    result["updater_binary_file"] = json.load(stream)
for name in ("baseline", "control"):
    def load(suffix):
        with open(os.path.join(scratch, name + suffix), "r", encoding="utf-8") as stream:
            return stream.read().strip()
    with open(os.path.join(scratch, name + ".properties.json"), "r", encoding="utf-8") as stream:
        properties = json.load(stream)
    with open(os.path.join(scratch, name + ".markers.json"), "r", encoding="utf-8") as stream:
        markers = json.load(stream)
    result["cases"].append({"case": name, "start": load(".start"),
                            "unit_properties": properties, "markers": markers})
with open(destination, "x", encoding="utf-8") as stream:
    json.dump(result, stream, separators=(",", ":"))
    stream.write("\n")
PY

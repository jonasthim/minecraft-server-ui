#!/usr/bin/env bash
# Disposable GitHub-hosted Ubuntu24.04 only. Never run on a user's installation.
set -euo pipefail
umask 077
[[ ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted && $EUID == 0 ]] || { echo 'FAIL hosted_root_required' >&2; exit 2; }
[[ $# == 3 ]] || { echo 'FAIL tuple_not_allowlisted' >&2; exit 2; }
case "$3" in 1.12.2|1.21.9) ;; *) echo 'FAIL tuple_not_allowlisted' >&2; exit 2 ;; esac
here=$(cd -- "$(dirname -- "$0")" && pwd)
repo=$(realpath "$here/../..")
[[ -f $1 && ! -L $1 && -d $2 && ! -L $2 ]] || exit 2
binary=$(realpath "$1")
output=$(realpath "$2")
while IFS= read -r -d '' entry; do
    case "${entry##*/}" in commit.txt|go-version.txt|runner.txt) ;; *) echo 'FAIL unexpected_report_file' >&2; exit 2 ;; esac
    [[ -f $entry && ! -L $entry && $(stat -c %s "$entry") -le 4096 ]] || exit 2
done < <(find "$output" -mindepth 1 -maxdepth 1 -print0)
# Output must not alias private runtime state or disappear during cleanup.
case "$output/" in /var/lib/minecraft-ui/*|/etc/minecraft-ui/*|/usr/local/lib/minecraft-ui/*) exit 2 ;; esac
for item in /var/lib/minecraft-ui /etc/minecraft-ui /usr/local/bin/minecraft-ui /usr/local/bin/minecraft-ui.new /usr/local/lib/minecraft-ui /etc/systemd/system/minecraft-ui.service /etc/systemd/system/minecraft-ui.service.d /etc/systemd/system/minecraft@.service /etc/sudoers.d/minecraft-ui; do
    [[ ! -e $item && ! -L $item ]] || { echo 'FAIL existing_installation' >&2; exit 2; }
done
! id minecraft-ui &>/dev/null || { echo 'FAIL existing_service_account' >&2; exit 2; }
! getent group minecraft-ui &>/dev/null || { echo 'FAIL existing_service_group' >&2; exit 2; }
. /etc/os-release
[[ $ID == ubuntu && $VERSION_ID == 24.04 && $(uname -m) == x86_64 ]] || { echo 'FAIL host_not_allowlisted' >&2; exit 2; }
scratch=$(mktemp -d /tmp/minecraft-game-acceptance.XXXXXX)
owned=0
borrowed_bin_mode=
borrowed_bin_identity=
borrowed_bin_prepared=0
firewall4=0
firewall6=0
stage=dependencies
binary_hash=$(sha256sum "$binary")
binary_hash=${binary_hash%% *}
record_permissions() {
    python3 - "$scratch/permissions.json" "$1" "$here" <<'PY'
import json
from pathlib import Path
import sys
sys.path.insert(0, sys.argv[3])
from game_api import permission_snapshot
path = Path(sys.argv[1])
history = json.loads(path.read_text()) if path.exists() else []
history.append(permission_snapshot(sys.argv[2]))
path.write_text(json.dumps(history))
PY
}
provision_hosted_prefix() {
    # Same reviewed disposable-host preparation as the updater harness. Never
    # relax the shipping supervisor's protected-ancestry requirement.
    local parent mode
    for parent in / /usr /usr/local /usr/local/bin; do
        [[ -d $parent && ! -L $parent && $(stat -c '%u' "$parent") == 0 ]] || return 1
        mode=$(stat -c '%a' "$parent")
        if [[ $parent != /usr/local/bin ]]; then [[ $((8#$mode & 0022)) == 0 ]] || return 1; fi
    done
    prepare_hosted_prefix_mode
}
prepare_hosted_prefix_mode() {
    local mode
    borrowed_bin_mode=$(stat -c '%a' /usr/local/bin)
    borrowed_bin_identity=$(stat -c '%d:%i:%u:%g' /usr/local/bin)
    borrowed_bin_prepared=1
    printf -v mode '%o' "$((8#$borrowed_bin_mode & ~0022))"
    chmod "$mode" /usr/local/bin
    [[ $(stat -c '%a' /usr/local/bin) == "$mode" && $(stat -c '%d:%i:%u:%g' /usr/local/bin) == "$borrowed_bin_identity" ]]
}
restore_hosted_prefix() {
    [[ $borrowed_bin_prepared == 1 ]] || return 0
    [[ -d /usr/local/bin && ! -L /usr/local/bin && $(stat -c '%d:%i:%u:%g' /usr/local/bin) == "$borrowed_bin_identity" ]] || return 1
    chmod "$borrowed_bin_mode" /usr/local/bin || return 1
    [[ $(stat -c '%a' /usr/local/bin) == "$borrowed_bin_mode" && $(stat -c '%d:%i:%u:%g' /usr/local/bin) == "$borrowed_bin_identity" ]]
}
cleanup() {
    local status=$?
    trap - EXIT
    set +e
    record_permissions before-cleanup || status=1
    if [[ $owned == 1 ]]; then
        timeout 170s systemctl stop minecraft-ui.service >"$scratch/manager-cleanup" 2>&1
        [[ $? == 0 ]] || status=1
        # Shipping Linux manager stop deliberately leaves independent games.
        timeout 120s systemctl stop minecraft@game-acceptance.service >"$scratch/game-cleanup" 2>&1
        [[ $? == 0 ]] || status=1
        systemctl disable minecraft@game-acceptance.service >"$scratch/disable" 2>&1
        rm -f /etc/systemd/system/multi-user.target.wants/minecraft@game-acceptance.service
        # Do not remove runtime state while any process still holds our UID.
        if pgrep -u minecraft-ui >/dev/null 2>&1; then
            echo 'FAIL orphan_service_process' >&2
            status=1
            # Fixed owned unit cgroups only; never kill a caller-selected PID.
            systemctl kill --kill-who=all --signal=SIGKILL minecraft@game-acceptance.service minecraft-ui.service >"$scratch/kill" 2>&1
            sleep 1
        fi
        if pgrep -u minecraft-ui >/dev/null 2>&1; then
            echo 'FAIL process_cleanup_incomplete' >&2
            status=1
        else
            rm -f /etc/systemd/system/minecraft-ui.service /etc/systemd/system/minecraft@.service /etc/sudoers.d/minecraft-ui /usr/local/bin/minecraft-ui /usr/local/bin/minecraft-ui.new
            rm -rf /etc/systemd/system/minecraft-ui.service.d /etc/minecraft-ui /usr/local/lib/minecraft-ui /var/lib/minecraft-ui
            userdel minecraft-ui >"$scratch/userdel" 2>&1
            [[ $? == 0 ]] || status=1
            groupdel minecraft-ui >/dev/null 2>&1
        fi
        systemctl daemon-reload >"$scratch/reload" 2>&1
        [[ $? == 0 ]] || status=1
        systemctl reset-failed minecraft-ui.service minecraft@game-acceptance.service >/dev/null 2>&1
        for item in /var/lib/minecraft-ui /etc/minecraft-ui /usr/local/bin/minecraft-ui /usr/local/bin/minecraft-ui.new /usr/local/lib/minecraft-ui /etc/systemd/system/minecraft-ui.service /etc/systemd/system/minecraft-ui.service.d /etc/systemd/system/minecraft@.service /etc/sudoers.d/minecraft-ui; do [[ ! -e $item && ! -L $item ]] || status=1; done
    fi
    if [[ $firewall4 == 1 ]]; then
        iptables -D INPUT ! -i lo -p tcp -m multiport --dports 8080,25565 -m comment --comment minecraft-game-acceptance -j DROP
        [[ $? == 0 ]] || status=1
    fi
    if [[ $firewall6 == 1 ]]; then
        ip6tables -D INPUT ! -i lo -p tcp -m multiport --dports 8080,25565 -m comment --comment minecraft-game-acceptance -j DROP
        [[ $? == 0 ]] || status=1
    fi
    restore_hosted_prefix || status=1
    record_permissions after-prefix-restoration || status=1
    python3 - "$status" "$stage" "$binary_hash" "$scratch/permissions.json" "$3" <<'PY' >"$output/result.json"
import json
from pathlib import Path
import sys
path = Path(sys.argv[4])
print(json.dumps({"scope": "vanilla-" + sys.argv[5] + "-real-headless-game", "minecraft_version": sys.argv[5], "exit_code": int(sys.argv[1]),
                  "last_stage": sys.argv[2], "manager_sha256": sys.argv[3],
                  "permission_snapshots": json.loads(path.read_text()) if path.exists() else []}))
PY
    chmod 0644 "$output/result.json"
    [[ ! -f $output/game-summary.json ]] || chmod 0644 "$output/game-summary.json"
    rm -rf "$scratch"
    exit "$status"
}
trap cleanup EXIT
trap 'exit 143' TERM
trap 'exit 130' INT
node -e 'if(Number(process.versions.node.split(".")[0])<22)process.exit(1)'
timeout 180s npm ci --ignore-scripts --prefix "$here/game-client" >"$scratch/npm" 2>&1
stage=firewall
iptables -I INPUT 1 ! -i lo -p tcp -m multiport --dports 8080,25565 -m comment --comment minecraft-game-acceptance -j DROP
firewall4=1
ip6tables -I INPUT 1 ! -i lo -p tcp -m multiport --dports 8080,25565 -m comment --comment minecraft-game-acceptance -j DROP
firewall6=1
iptables -C INPUT ! -i lo -p tcp -m multiport --dports 8080,25565 -m comment --comment minecraft-game-acceptance -j DROP
ip6tables -C INPUT ! -i lo -p tcp -m multiport --dports 8080,25565 -m comment --comment minecraft-game-acceptance -j DROP
stage=prefix_preparation
record_permissions before-prefix-provisioning
provision_hosted_prefix
record_permissions after-prefix-provisioning
stage=installer
owned=1
bash "$repo/deploy/linux/install.sh" "$binary" >"$scratch/install" 2>&1
stage=native_start
timeout 90s systemctl start minecraft-ui.service >"$scratch/start" 2>&1
stage=game_api
timeout 28m python3 "$here/game_api.py" --minecraft-version "$3" --output "$output"
stage=complete

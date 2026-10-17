#!/usr/bin/env bash
# Owns only fresh canonical state on a disposable GitHub-hosted VM.
set -euo pipefail
umask 077
[[ ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted && $EUID == 0 ]] || { echo 'Disposable hosted runner/root required.' >&2; exit 2; }
[[ $# == 2 || $# == 3 ]] || exit 2
binary=$(realpath "$1")
output=$(realpath "$2")
baseline=${3:-}
here=$(cd -- "$(dirname -- "$0")" && pwd)
repo=$(realpath "$here/../..")
[[ -f $binary && -d $output && ! -L $output ]] || exit 2
for item in /var/lib/minecraft-ui /etc/minecraft-ui /usr/local/bin/minecraft-ui /usr/local/lib/minecraft-ui /etc/systemd/system/minecraft-ui.service /etc/systemd/system/minecraft-ui.service.d /etc/systemd/system/minecraft@.service /etc/systemd/system/multi-user.target.wants/minecraft@delivery-smoke.service /etc/sudoers.d/minecraft-ui; do
    [[ ! -e $item && ! -L $item ]] || { echo 'Refusing existing installation.' >&2; exit 2; }
done
! id minecraft-ui &>/dev/null || { echo 'Refusing existing service account.' >&2; exit 2; }
! getent group minecraft-ui &>/dev/null || { echo 'Refusing existing service group.' >&2; exit 2; }
owned=0
scratch=$(mktemp -d /tmp/minecraft-native-smoke.XXXXXX)
cleanup() {
    local status=$?
    trap - EXIT
    set +e
    if [[ $owned == 1 ]]; then
        # Only fixed/allowlisted diagnostics. Never copy DB, environment, raw
        # journal, cookie jars, sessions or arbitrary application data.
        systemctl show minecraft-ui.service -p User -p Group -p MainPID -p ActiveState -p SubState -p Result -p ExecMainCode -p ExecMainStatus -p NoNewPrivileges -p ProtectSystem -p ProtectHome -p ProtectControlGroups -p PrivateTmp -p ReadOnlyPaths -p InaccessiblePaths >"$output/unit-properties.txt"
        journalctl -u minecraft-ui.service --no-pager -o json | python3 "$here/summarize-journal.py" >"$output/journal-summary.json"
        for phase in baseline candidate; do
            [[ ! -f /var/lib/minecraft-ui/native-smoke-$phase.txt ]] || cp "/var/lib/minecraft-ui/native-smoke-$phase.txt" "$output/$phase-helper.txt"
        done
        timeout 170s systemctl stop minecraft-ui.service
        [[ $? == 0 ]] || status=1
        systemctl disable minecraft@delivery-smoke.service >/dev/null 2>&1
        rm -f /etc/systemd/system/multi-user.target.wants/minecraft@delivery-smoke.service
        rm -f /etc/systemd/system/minecraft-ui.service /etc/systemd/system/minecraft@.service /etc/sudoers.d/minecraft-ui /usr/local/bin/minecraft-ui
        rm -rf /etc/systemd/system/minecraft-ui.service.d /etc/minecraft-ui /usr/local/lib/minecraft-ui /var/lib/minecraft-ui
        systemctl daemon-reload
        [[ $? == 0 ]] || status=1
        systemctl reset-failed minecraft-ui.service >/dev/null 2>&1
        userdel minecraft-ui
        [[ $? == 0 ]] || status=1
        groupdel minecraft-ui >/dev/null 2>&1 # userdel can remove the private group.
        if pgrep -x minecraft-ui >/dev/null; then
            echo 'FAIL orphan manager at cleanup' >&2
            status=1
        fi
    fi
    rm -rf "$scratch"
    printf '{"exit_code":%s,"scope":"installed-manager-only"}\n' "$status" >"$output/result.json"
    # Artifact uploader runs as the runner user. These are scrubbed reports,
    # not service state; leave no root-only report silently uncollected.
    find "$output" -maxdepth 1 -type f \( -name '*.txt' -o -name '*.json' \) -exec chmod 0644 {} +
    exit "$status"
}
trap cleanup EXIT
systemctl --version >"$output/systemd-version.txt"
sha256sum "$binary" >"$output/manager-sha256.txt"
"$binary" version >"$output/manager-version.txt"
install -d "$scratch/bundle"
cp "$repo"/deploy/linux/* "$scratch/bundle/"
owned=1
if [[ -n $baseline ]]; then
    [[ -f $baseline ]] || exit 2
    cp "$baseline" "$scratch/bundle/minecraft-ui.service"
fi
bash "$scratch/bundle/install.sh" "$binary"
install -m 0755 -o root -g root "$here/unit-probe.sh" /usr/local/lib/minecraft-ui/native-smoke-probe
install -d -m 0755 /etc/systemd/system/minecraft-ui.service.d
set_probe() {
    printf '[Service]\nExecStartPost=/usr/local/lib/minecraft-ui/native-smoke-probe %s\n' "$1" >/etc/systemd/system/minecraft-ui.service.d/native-smoke.conf
    systemctl daemon-reload
}
if [[ -n $baseline ]]; then
    set_probe baseline
    timeout 90s systemctl start minecraft-ui.service
    pid=$(systemctl show -p MainPID --value minecraft-ui.service)
    awk '/^(Uid|Gid|NoNewPrivs):/' "/proc/$pid/status" >"$output/baseline-process.txt"
    timeout 170s systemctl stop minecraft-ui.service
    # Root cleanup remains necessary when sudo was refused inside the unit.
    systemctl disable minecraft@delivery-smoke.service >/dev/null 2>&1
    install -o root -g root -m 0644 "$repo/deploy/linux/minecraft-ui.service" /etc/systemd/system/minecraft-ui.service
fi
set_probe candidate
timeout 90s systemctl start minecraft-ui.service
pid=$(systemctl show -p MainPID --value minecraft-ui.service)
[[ $pid -gt 1 ]]
awk '/^(Uid|Gid|NoNewPrivs):/' "/proc/$pid/status" >"$output/candidate-process.txt"
[[ $(awk '/^NoNewPrivs:/ {print $2}' "/proc/$pid/status") == 0 ]]
[[ $(awk '/^Uid:/ {print $2}' "/proc/$pid/status") == "$(id -u minecraft-ui)" ]]
[[ $(id -u minecraft-ui) != 0 ]]
[[ $(stat -c '%U:%G:%a' /var/lib/minecraft-ui) == minecraft-ui:minecraft-ui:700 ]]
[[ $(stat -c '%U:%G:%a' /usr/local/bin/minecraft-ui) == root:root:755 ]]
[[ $(stat -c '%U:%G:%a' /usr/local/lib/minecraft-ui/unitctl) == root:root:755 ]]
[[ $(stat -c '%U:%G:%a' /etc/sudoers.d/minecraft-ui) == root:root:440 ]]
[[ ! -e /etc/systemd/system/multi-user.target.wants/minecraft@delivery-smoke.service ]]
# Remove probe after its acceptance assertions so lifecycle checks use the
# shipping unit alone; the helper was already exercised in its actual sandbox.
rm /etc/systemd/system/minecraft-ui.service.d/native-smoke.conf
systemctl daemon-reload
python3 "$here/manager_api.py" | tee "$output/api-assertions.txt"
[[ $(stat -c '%U:%G:%a' /var/lib/minecraft-ui/manager.db) == minecraft-ui:minecraft-ui:600 ]]
echo 'PASS installed Linux identity, permissions, actual-unit helper, lifecycle'

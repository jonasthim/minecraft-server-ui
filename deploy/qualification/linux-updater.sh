#!/usr/bin/env bash
# Task32B: never invoke outside a fresh disposable hosted VM. No Minecraft runs.
set -euo pipefail
umask 077
[[ ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted && $EUID == 0 ]] || { echo 'Disposable hosted runner/root required.' >&2; exit 2; }
[[ $# == 8 ]] || { echo 'Expected INITIAL SUCCESS UPDATER QUALIFIER NEGATIVE PEER_SUCCESS PEER_NEGATIVE REPORT_DIR.' >&2; exit 2; }
initial=$(realpath "$1")
success=$(realpath "$2")
updater=$(realpath "$3")
qualifier=$(realpath "$4")
negative=$(realpath "$5")
peer_success=$(realpath "$6")
peer_negative=$(realpath "$7")
output=$(realpath "$8")
here=$(cd -- "$(dirname -- "$0")" && pwd)
repo=$(realpath "$here/../..")
[[ -d $output && ! -L $output ]] || exit 2
for binary in "$initial" "$success" "$updater" "$qualifier" "$negative" "$peer_success" "$peer_negative"; do
    [[ -f $binary && ! -L $binary ]] || exit 2
done
files=(
 /var/lib/minecraft-ui /var/lib/minecraft-ui-update /etc/minecraft-ui
 /usr/local/bin/minecraft-ui /usr/local/bin/minecraft-ui.new /usr/local/bin/minecraft-ui.next /usr/local/bin/minecraft-ui.previous
 /usr/local/lib/minecraft-ui /etc/sudoers.d/minecraft-ui
 /etc/systemd/system/minecraft-ui.service /etc/systemd/system/minecraft-ui.service.d
 /etc/systemd/system/minecraft@.service /etc/systemd/system/minecraft-ui-update@.service
 /etc/systemd/system/minecraft-ui-update-recovery.service
 /etc/systemd/system/minecraft-ui-qualification-independent.service
)
for item in "${files[@]}"; do
    [[ ! -e $item && ! -L $item ]] || { echo 'Refusing existing canonical installation/state.' >&2; exit 2; }
done
! id minecraft-ui &>/dev/null || { echo 'Refusing existing manager account.' >&2; exit 2; }
! getent group minecraft-ui &>/dev/null || { echo 'Refusing existing manager group.' >&2; exit 2; }
for unit in minecraft-ui.service minecraft-ui-update@.service minecraft-ui-update-recovery.service minecraft-ui-qualification-independent.service minecraft@.service; do
    [[ -z $(systemctl list-unit-files --no-legend --no-pager "$unit") ]] || { echo 'Refusing preexisting native service.' >&2; exit 2; }
done
[[ -z $(systemctl list-units --all --plain --no-legend --no-pager 'minecraft-ui-update@*.service') ]] || { echo 'Refusing preexisting updater operation.' >&2; exit 2; }
scratch=$(mktemp -d /tmp/minecraft-updater-acceptance.XXXXXX)
owned=0
borrowed_bin_mode=
borrowed_bin_identity=
borrowed_bin_prepared=0
record_permissions() {
    python3 "$here/updater_permissions.py" "$output/permission-summary-linux.json" "$1"
}
provision_hosted_prefix() {
    # Explicit disposable-image provisioning: never silently change a production
    # prefix or relax the shipping updater's root-protected-ancestry requirement.
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
        # Fixed fields only. No raw journals, challenges, configs, keys or DB.
        systemctl show minecraft-ui.service -p User -p Group -p MainPID -p ActiveState -p SubState -p Result -p ExecMainStatus -p NoNewPrivileges -p ProtectSystem -p ProtectHome -p ProtectControlGroups -p PrivateTmp >"$output/manager-unit-summary.txt"
        systemctl show 'minecraft-ui-update@*.service' minecraft-ui-update-recovery.service -p Id -p MainPID -p ActiveState -p SubState -p Result -p ExecMainStatus >"$output/updater-unit-summary.txt"
        systemctl show 'minecraft-ui-update@*.service' minecraft-ui-update-recovery.service -p Id -p User -p CapabilityBoundingSet -p PrivateUsers -p SystemCallFilter -p RestrictSUIDSGID -p NoNewPrivileges | python3 "$here/summarize-updater-unit.py" >"$output/updater-context-properties.json"
        journalctl -u minecraft-ui.service -u 'minecraft-ui-update@*' -u minecraft-ui-update-recovery.service --no-pager -o json | python3 "$here/summarize-journal.py" >"$output/journal-summary.json"
        timeout 15s systemctl kill --signal=SIGKILL 'minecraft-ui-update@*.service' >/dev/null 2>&1
        timeout 180s systemctl stop minecraft-ui.service minecraft-ui-update-recovery.service minecraft-ui-qualification-independent.service >/dev/null 2>&1 || status=1
        # Catch deleted-image processes too; the Python acceptance covers exact
        # native identity during each activation and before this final cleanup.
        if pgrep -x minecraft-ui >/dev/null; then echo 'FAIL orphan manager during cleanup' >&2; status=1; fi
        rm -f /usr/local/bin/minecraft-ui /usr/local/bin/minecraft-ui.new /usr/local/bin/minecraft-ui.next /usr/local/bin/minecraft-ui.previous
        rm -f /etc/systemd/system/minecraft-ui.service /etc/systemd/system/minecraft@.service /etc/systemd/system/minecraft-ui-update@.service /etc/systemd/system/minecraft-ui-update-recovery.service /etc/systemd/system/minecraft-ui-qualification-independent.service /etc/sudoers.d/minecraft-ui
        rm -rf /etc/systemd/system/minecraft-ui.service.d /etc/minecraft-ui /usr/local/lib/minecraft-ui /var/lib/minecraft-ui /var/lib/minecraft-ui-update
        systemctl daemon-reload || status=1
        systemctl reset-failed minecraft-ui.service minecraft-ui-update-recovery.service minecraft-ui-qualification-independent.service 'minecraft-ui-update@*.service' >/dev/null 2>&1
        if id minecraft-ui &>/dev/null; then userdel minecraft-ui || status=1; fi
        if getent group minecraft-ui &>/dev/null; then groupdel minecraft-ui || status=1; fi
        for item in "${files[@]}"; do [[ ! -e $item && ! -L $item ]] || status=1; done
        id minecraft-ui &>/dev/null && status=1
        getent group minecraft-ui &>/dev/null && status=1
    fi
    restore_hosted_prefix || status=1
    record_permissions after-prefix-restoration || status=1
    rm -rf "$scratch"
    printf '{"exit_code":%s,"scope":"installed-updater-fixture-acquisition","cleanup_checked":true}\n' "$status" >"$output/result.json"
    for report in result.json acceptance-summary.json interruption-boundary.json activation-state-summary.json initial-update-status.json api-assertions.txt manager-unit-summary.txt updater-unit-summary.txt updater-context-properties.json journal-summary.json input-summary.txt permission-summary-linux.json; do
        [[ ! -f $output/$report ]] || chmod 0644 "$output/$report"
    done
    exit "$status"
}
trap cleanup EXIT
"$initial" version >"$scratch/initial-version"
"$success" version >"$scratch/success-version"
[[ $(cat "$scratch/initial-version") == 'minecraft-ui v0.0.1' && $(cat "$scratch/success-version") == 'minecraft-ui v0.0.2' ]]
{
    printf '%s\n' 'scope=fixture acquisition; installed native activation/recovery; no game evidence'
    cat "$scratch/initial-version" "$scratch/success-version"
    "$updater" version
    systemctl --version
    dpkg-query -W -f='${binary:Package} ${Version}\n' util-linux libpam-modules libpam0g
    for binary in "$initial" "$success" "$updater" "$qualifier" "$negative" "$peer_success" "$peer_negative"; do sha256sum "$binary" | cut -d' ' -f1; done
} >"$output/input-summary.txt"
"$qualifier" prepare --success "$success" --negative "$negative" --peer-success "$peer_success" --peer-negative "$peer_negative" --license "$repo/LICENSE" --notices "$repo/THIRD_PARTY_NOTICES.txt" --out "$scratch/fixtures"
record_permissions before-prefix-provisioning
provision_hosted_prefix
record_permissions after-prefix-provisioning
owned=1
bash "$repo/deploy/linux/install.sh" "$initial" "$updater"
install -o root -g root -m 0644 "$scratch/fixtures/public-trust.json" /etc/minecraft-ui/update-trust.json
install -o root -g root -m 0755 "$qualifier" /usr/local/lib/minecraft-ui/qualification-updater
cp -r "$scratch/fixtures" /var/lib/minecraft-ui/selfupdate/qualification-input
chown -R minecraft-ui:minecraft-ui /var/lib/minecraft-ui/selfupdate/qualification-input
for version in v0.0.2 v0.0.3; do
    runuser -u minecraft-ui -- /usr/local/lib/minecraft-ui/qualification-updater stage --bundle /var/lib/minecraft-ui/selfupdate/qualification-input --version "$version" --current v0.0.1 --stage-dir /var/lib/minecraft-ui/selfupdate/stages >"$scratch/$version-stage.json"
done
[[ $(stat -c '%U:%G:%a' /var/lib/minecraft-ui-update) == root:root:700 ]]
[[ $(stat -c '%U:%G:%a' /var/lib/minecraft-ui-update/stages) == root:root:700 ]]
[[ $(stat -c '%U:%G:%a' /var/lib/minecraft-ui/selfupdate/stages) == minecraft-ui:minecraft-ui:700 ]]
[[ $(stat -c '%U:%G:%a' /usr/local/lib/minecraft-ui/selfupdate) == root:root:755 ]]
[[ $(stat -c '%U:%G:%a' /etc/minecraft-ui/update-trust.json) == root:root:644 ]]
cmp "$repo/deploy/linux/minecraft@.service" /etc/systemd/system/minecraft@.service
cmp "$repo/deploy/linux/minecraft-ui.service" /etc/systemd/system/minecraft-ui.service
# Clearly labelled process-lifetime fixture, independent of the manager unit.
# It establishes independent native service lifetime only, never game support.
cat >/etc/systemd/system/minecraft-ui-qualification-independent.service <<'UNIT'
[Unit]
Description=Task32B independent dummy service (not Minecraft)
[Service]
Type=simple
User=minecraft-ui
Group=minecraft-ui
ExecStart=/usr/bin/sleep infinity
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ProtectControlGroups=true
UNIT
systemctl daemon-reload
timeout 90s systemctl start minecraft-ui-qualification-independent.service minecraft-ui.service
[[ $(systemctl show --property=NoNewPrivileges --value minecraft-ui.service) == no ]]
[[ $(systemctl show --property=ProtectSystem --value minecraft-ui.service) == strict ]]
[[ $(systemctl show --property=ProtectHome --value minecraft-ui.service) == yes ]]
[[ $(systemctl show --property=PrivateTmp --value minecraft-ui.service) == yes ]]
[[ $(systemctl show --property=ProtectControlGroups --value minecraft-ui.service) == yes ]]
python3 "$here/updater_api.py" --success-stage "$scratch/v0.0.2-stage.json" --negative-stage "$scratch/v0.0.3-stage.json" --initial-binary "$initial" --success-binary "$success" --negative-binary "$negative" --output "$output" | tee "$output/api-assertions.txt"
echo 'PASS installed Linux updater fixture acquisition, genuine activation, negative health rollback and interrupted candidate recovery'

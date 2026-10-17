#!/usr/bin/env bash
# Run only on a fresh disposable GitHub-hosted Ubuntu runner as root.
set -euo pipefail
umask 077

[[ ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted && $EUID == 0 ]] || { echo 'Disposable GitHub-hosted runner/root required.' >&2; exit 2; }
[[ $# == 4 || $# == 7 ]] || { echo 'Expected BOOTSTRAP CANDIDATES REPORT_DIR SCENARIO [QUALIFIER LINUX_INPUTS WINDOWS_INPUTS].' >&2; exit 2; }
bootstrap=$(realpath -e -- "$1")
candidates=$(realpath -e -- "$2")
output=$(realpath -e -- "$3")
scenario=$4
here=$(cd -- "$(dirname -- "$0")" && pwd)
repo=$(realpath "$here/../..")
case "$scenario" in lifecycle|retained-retry|updater-coexistence) ;; *) echo 'Unknown qualification scenario.' >&2; exit 2 ;; esac
[[ -f $bootstrap && ! -L $bootstrap && -d $candidates && ! -L $candidates && -d $output && ! -L $output ]] || { echo 'Ordinary fixture inputs and report directory required.' >&2; exit 2; }
[[ $(realpath -e -- "$bootstrap") == "$repo/"* && $(realpath -e -- "$candidates") == "$repo/"* && $(realpath -e -- "$output") == "$repo/"* ]] || { echo 'Qualification paths must remain in the workspace.' >&2; exit 2; }
extra_args=()
if [[ $scenario == updater-coexistence ]]; then
  [[ $# == 7 ]] || { echo 'Updater coexistence requires explicit qualifier and native peer input directories.' >&2; exit 2; }
  qualifier=$(realpath -e -- "$5")
  linux_inputs=$(realpath -e -- "$6")
  windows_inputs=$(realpath -e -- "$7")
  [[ -f $qualifier && ! -L $qualifier && -x $qualifier && -d $linux_inputs && ! -L $linux_inputs && -d $windows_inputs && ! -L $windows_inputs ]] || { echo 'Ordinary updater fixture inputs required.' >&2; exit 2; }
  [[ $qualifier == "$repo/"* && $linux_inputs == "$repo/"* && $windows_inputs == "$repo/"* ]] || { echo 'Updater fixture inputs must remain in the workspace.' >&2; exit 2; }
  extra_args=(--qualifier "$qualifier" --linux-inputs "$linux_inputs" --windows-inputs "$windows_inputs")
else
  [[ $# == 4 ]] || { echo 'Updater fixture paths are only accepted for updater-coexistence.' >&2; exit 2; }
fi
for report in "$output"/*; do
  [[ -e $report || -L $report ]] || continue
  case "$(basename -- "$report")" in source-commit.txt|runner.txt) [[ -f $report && ! -L $report ]] || exit 2 ;; *) echo 'Report directory contains an unapproved file.' >&2; exit 2 ;; esac
done
[[ $(stat -c '%u:%g' /usr/local/bin) == 0:0 ]] || { echo 'Borrowed /usr/local/bin is not root-owned.' >&2; exit 2; }
prefix_mode=$(stat -c '%a' /usr/local/bin)
for parent in / /usr /usr/local /usr/local/bin; do
  [[ -d $parent && ! -L $parent && $(stat -c '%u' "$parent") == 0 ]] || { echo 'Borrowed installation ancestry is not root-owned and ordinary.' >&2; exit 2; }
  mode=$(stat -c '%a' "$parent")
  if [[ $parent != /usr/local/bin ]]; then [[ $((8#$mode & 0022)) == 0 ]] || { echo 'Borrowed installation ancestry is writable by group/world.' >&2; exit 2; }; fi
done
prefix_identity=$(stat -c '%d:%i:%u:%g' /usr/local/bin)
prefix_prepared=0
printf -v safe_prefix_mode '%o' "$((8#$prefix_mode & ~0022))"

files=(
  /var/lib/minecraft-ui /var/lib/minecraft-ui-update /etc/minecraft-ui
  /usr/local/bin/minecraft-ui /usr/local/bin/minecraft-ui.new /usr/local/bin/minecraft-ui.next /usr/local/bin/minecraft-ui.previous
  /usr/local/lib/minecraft-ui /etc/sudoers.d/minecraft-ui
  /etc/systemd/system/minecraft-ui.service /etc/systemd/system/minecraft-ui.service.d
  /etc/systemd/system/minecraft@.service /etc/systemd/system/minecraft-ui-update@.service
  /etc/systemd/system/minecraft-ui-update-recovery.service
  /etc/systemd/system/minecraft@installer-fixture.service.d
  /etc/systemd/system/minecraft-ui-qualification-independent.service
)
for item in "${files[@]}"; do [[ ! -e $item && ! -L $item ]] || { echo 'Refusing preexisting canonical fixture state.' >&2; exit 2; }; done
! id minecraft-ui &>/dev/null || { echo 'Refusing preexisting manager account.' >&2; exit 2; }
! getent group minecraft-ui &>/dev/null || { echo 'Refusing preexisting manager group.' >&2; exit 2; }
for unit in minecraft-ui.service minecraft@.service minecraft-ui-update@.service minecraft-ui-update-recovery.service minecraft-ui-qualification-independent.service; do
  [[ -z $(systemctl list-unit-files --no-legend --no-pager "$unit") ]] || { echo 'Refusing preexisting native unit.' >&2; exit 2; }
done
[[ -z $(systemctl list-units --all --plain --no-legend --no-pager 'minecraft-ui-install-*.service') ]] || { echo 'Refusing preexisting installer worker unit.' >&2; exit 2; }
[[ -z $(systemctl list-units --all --plain --no-legend --no-pager 'minecraft-ui-update@*.service') ]] || { echo 'Refusing preexisting updater worker unit.' >&2; exit 2; }
[[ -z $(systemctl list-units --all --plain --no-legend --no-pager 'minecraft@*.service') ]] || { echo 'Refusing preexisting managed game unit.' >&2; exit 2; }
[[ -z $(systemctl list-units --all --plain --no-legend --no-pager minecraft-ui-qualification-independent.service) ]] || { echo 'Refusing preexisting updater coexistence fixture unit.' >&2; exit 2; }

owned=1
diagnostics_saved=0
diagnostics() {
  [[ $diagnostics_saved == 0 ]] || return 0
  diagnostics_saved=1
  python3 "$here/linux_installer_diagnostics.py" "$output"
}
cleanup() {
  local result=$?
  trap - EXIT
  set +e
  if [[ $owned == 1 ]]; then
    diagnostics || result=1
    timeout 20s systemctl stop minecraft@installer-fixture.service >/dev/null 2>&1
    timeout 30s systemctl stop minecraft-ui-qualification-independent.service >/dev/null 2>&1
    if systemctl show --property=MainPID --value minecraft-ui-qualification-independent.service >/dev/null 2>&1; then
      independent_pid=$(systemctl show --property=MainPID --value minecraft-ui-qualification-independent.service 2>/dev/null)
      independent_state=$(systemctl show --property=ActiveState --value minecraft-ui-qualification-independent.service 2>/dev/null)
    else
      independent_pid=0
      independent_state=inactive
    fi
    if [[ $independent_pid =~ ^[0-9]+$ && $independent_pid -gt 1 ]]; then
      timeout 15s systemctl kill --kill-who=all --signal=SIGKILL minecraft-ui-qualification-independent.service >/dev/null 2>&1
      timeout 30s systemctl stop minecraft-ui-qualification-independent.service >/dev/null 2>&1
      independent_pid=$(systemctl show --property=MainPID --value minecraft-ui-qualification-independent.service 2>/dev/null)
      independent_state=$(systemctl show --property=ActiveState --value minecraft-ui-qualification-independent.service 2>/dev/null)
    fi
    independent_stopped=1
    if [[ $independent_pid != 0 || ( $independent_state != inactive && $independent_state != failed ) ]]; then
      independent_stopped=0
      result=1
    fi
    workers=()
    # This pattern is constrained to operation units created by the fixed
    # installer prefix; the preflight proved none existed before this run.
    while IFS= read -r unit; do
      [[ $unit =~ ^minecraft-ui-install-[0-9a-f]{64}\.service$ ]] || continue
      workers+=("$unit")
      timeout 15s systemctl kill --kill-who=all --signal=SIGKILL "$unit" >/dev/null 2>&1
      timeout 15s systemctl stop "$unit" >/dev/null 2>&1
    done < <(systemctl list-units --all --plain --no-legend --no-pager 'minecraft-ui-install-*.service' | awk '{print $1}')
    while IFS= read -r unit; do
      [[ $unit =~ ^minecraft-ui-update@[0-9a-f]{64}\.service$ ]] || continue
      workers+=("$unit")
      timeout 15s systemctl kill --kill-who=all --signal=SIGKILL "$unit" >/dev/null 2>&1
      timeout 15s systemctl stop "$unit" >/dev/null 2>&1
    done < <(systemctl list-units --all --plain --no-legend --no-pager 'minecraft-ui-update@*.service' | awk '{print $1}')
    workers_stopped=1
    for unit in "${workers[@]}"; do
      waited=0
      while (( waited < 30 )); do
        unit_state=$(systemctl show --property=ActiveState --value "$unit" 2>/dev/null)
        unit_pid=$(systemctl show --property=MainPID --value "$unit" 2>/dev/null)
        if [[ $unit_pid == 0 && ( $unit_state == inactive || $unit_state == failed ) ]]; then break; fi
        sleep 1
        ((waited+=1))
      done
      if [[ $unit_pid != 0 || ( $unit_state != inactive && $unit_state != failed ) ]]; then
        workers_stopped=0
        result=1
      fi
    done
    timeout 180s systemctl stop minecraft-ui.service minecraft-ui-update-recovery.service >/dev/null 2>&1
    if systemctl show --property=MainPID --value minecraft-ui.service >/dev/null 2>&1; then
      main_pid=$(systemctl show --property=MainPID --value minecraft-ui.service 2>/dev/null)
      active_state=$(systemctl show --property=ActiveState --value minecraft-ui.service 2>/dev/null)
    else
      main_pid=0
      active_state=inactive
    fi
    if [[ $main_pid =~ ^[0-9]+$ && $main_pid -gt 1 ]]; then
      timeout 15s systemctl kill --kill-who=all --signal=SIGKILL minecraft-ui.service >/dev/null 2>&1
      timeout 30s systemctl stop minecraft-ui.service >/dev/null 2>&1
      main_pid=$(systemctl show --property=MainPID --value minecraft-ui.service 2>/dev/null)
      active_state=$(systemctl show --property=ActiveState --value minecraft-ui.service 2>/dev/null)
    fi
    if [[ $workers_stopped != 1 || $independent_stopped != 1 || $main_pid != 0 || ( $active_state != inactive && $active_state != failed ) ]]; then
      echo 'Refusing to remove manager data while its service or installer worker may still be active.' >&2
      result=1
    else
      rm -f /usr/local/bin/minecraft-ui /usr/local/bin/minecraft-ui.new /usr/local/bin/minecraft-ui.next /usr/local/bin/minecraft-ui.previous
      rm -f /etc/systemd/system/minecraft-ui.service /etc/systemd/system/minecraft@.service /etc/systemd/system/minecraft-ui-update@.service /etc/systemd/system/minecraft-ui-update-recovery.service /etc/systemd/system/minecraft-ui-qualification-independent.service /etc/sudoers.d/minecraft-ui
      rm -rf /etc/systemd/system/minecraft-ui.service.d /etc/systemd/system/minecraft@installer-fixture.service.d
      rm -rf /etc/minecraft-ui /usr/local/lib/minecraft-ui /var/lib/minecraft-ui /var/lib/minecraft-ui-update
      systemctl daemon-reload >/dev/null 2>&1 || result=1
      systemctl reset-failed minecraft-ui.service minecraft-ui-update-recovery.service minecraft@installer-fixture.service minecraft-ui-qualification-independent.service >/dev/null 2>&1
      if id minecraft-ui &>/dev/null; then
        passwd=$(getent passwd minecraft-ui)
        if [[ $passwd == minecraft-ui:*:*:*:*:/var/lib/minecraft-ui:/usr/sbin/nologin ]]; then
          userdel minecraft-ui >/dev/null 2>&1 || result=1
        else
          echo 'Refusing to delete an account outside the exact fixture home and shell.' >&2
          result=1
        fi
      fi
      if getent group minecraft-ui &>/dev/null; then
        groupdel minecraft-ui >/dev/null 2>&1 || result=1
      fi
    fi
    for item in "${files[@]}"; do [[ ! -e $item && ! -L $item ]] || result=1; done
    id minecraft-ui &>/dev/null && result=1
    getent group minecraft-ui &>/dev/null && result=1
  fi
  if [[ $prefix_prepared == 1 ]]; then
    if [[ -d /usr/local/bin && ! -L /usr/local/bin && $(stat -c '%d:%i:%u:%g' /usr/local/bin) == "$prefix_identity" ]]; then
      chmod "$prefix_mode" /usr/local/bin || result=1
      [[ $(stat -c '%a' /usr/local/bin) == "$prefix_mode" ]] || result=1
    else
      echo 'Borrowed /usr/local/bin identity changed; refusing metadata restoration.' >&2
      result=1
    fi
  fi
  [[ $(stat -c '%d:%i:%u:%g' /usr/local/bin 2>/dev/null) == "$prefix_identity" ]] || result=1
  printf '{"exit_code":%s,"scope":"linux-installer-fixture-cleanup","fixed_paths_checked":true}\n' "$result" >"$output/cleanup.json"
  chmod 0644 "$output/cleanup.json" "$output/acceptance-summary.json" "$output/diagnostics.json" 2>/dev/null
  exit "$result"
}
trap cleanup EXIT

prefix_prepared=1
chmod "$safe_prefix_mode" /usr/local/bin
[[ $(stat -c '%a' /usr/local/bin) == "$safe_prefix_mode" && $(stat -c '%d:%i:%u:%g' /usr/local/bin) == "$prefix_identity" ]]
python3 "$here/linux_installer.py" "$bootstrap" "$candidates" "$output" "$scenario" "${extra_args[@]}"

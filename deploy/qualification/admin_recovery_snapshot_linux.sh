#!/usr/bin/env bash
# Private stopped-manager snapshot. Only the outer native harness owns this state.
set -euo pipefail
umask 077
[[ ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted && $EUID == 0 && $# == 1 ]] || exit 2
data=/var/lib/minecraft-ui
snapshot=$1
[[ $snapshot == "$data"/admin-recovery-* && $(dirname -- "$snapshot") == "$data" ]] || exit 2
[[ -d $snapshot && ! -L $snapshot && -z $(find "$snapshot" -mindepth 1 -print -quit) ]] || exit 2
[[ -d $data && ! -L $data && -f $data/manager.db && ! -L $data/manager.db && -f $data/manager.lock && ! -L $data/manager.lock ]] || exit 2
[[ $(systemctl is-active minecraft-ui.service) == inactive ]] || exit 2
exec 9<> "$data/manager.lock"
flock --exclusive --nonblock 9
# A second independent lock attempt must refuse while the snapshot owns it.
if flock --exclusive --nonblock "$data/manager.lock" true; then exit 1; fi
count=0
for name in manager.db manager.db-wal manager.db-shm manager.db-journal; do
    source=$data/$name
    if [[ -e $source || -L $source ]]; then
        [[ -f $source && ! -L $source ]] || exit 2
        cp -p -- "$source" "$snapshot/$name"
        original=$(sha256sum -- "$source")
        copied=$(sha256sum -- "$snapshot/$name")
        [[ ${original%% *} == "${copied%% *}" ]] || exit 1
        count=$((count + 1))
    fi
done
[[ -s $snapshot/manager.db ]]
# Only fixed fields/counts; never hashes, paths or file contents.
printf '{"snapshot_files":%s,"complete_sidecars":true,"hash_match":true,"lock_contention_refused":true}\n' "$count"

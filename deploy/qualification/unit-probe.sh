#!/bin/sh
# Test-only ExecStartPost, with the ACTUAL manager User= and sandbox directives.
# Never prefixes ExecStartPost with + or ! and never substitutes a fake helper.
set -eu
umask 077
phase=$1
case "$phase" in baseline|candidate) ;; *) exit 2 ;; esac
report=/var/lib/minecraft-ui/native-smoke-$phase.txt
exec >"$report" 2>&1
echo "phase=$phase"
echo "uid=$(id -u)"
awk '/^NoNewPrivs:/ { print "probe_" $0 }' /proc/self/status
failed=0
probe() {
    label=$1
    expected=$2
    shift 2
    rc=0
    /usr/bin/timeout 20s /usr/bin/sudo -n /usr/local/lib/minecraft-ui/unitctl "$@" >/dev/null 2>&1 || rc=$?
    echo "$label=$rc"
    [ "$rc" -eq "$expected" ] || failed=1
}
probe enable 0 enable delivery-smoke
probe disable 0 disable delivery-smoke
# A valid sudo grant must reach the helper's own validation and return 2.
probe invalid-verb 2 reboot delivery-smoke
probe invalid-id 2 enable ../escape
probe reserved-id 2 enable con
probe extra-argument 2 enable delivery-smoke surplus
echo "assertion_failures=$failed"
# Baseline is an explicitly labelled observation, never candidate acceptance.
[ "$phase" = baseline ] || [ "$failed" = 0 ]

#!/bin/sh
# Lifecycle commands for an existing authenticated Linux installation.
# First install: use the independently trusted release bootstrap with --install.
set -eu
[ "$(id -u)" = 0 ] || { echo 'Run as root.' >&2; exit 1; }
[ "$#" -gt 0 ] || { echo 'usage: install.sh apply|check|uninstall|recover [options]' >&2; exit 2; }
case "$1" in apply|check|uninstall|recover) ;; *) echo 'Unknown installer command.' >&2; exit 2 ;; esac
helper=/usr/local/lib/minecraft-ui/selfupdate
[ -f "$helper" ] && [ ! -L "$helper" ] && [ -x "$helper" ] || {
    echo 'Use the independently trusted release bootstrap for first installation or recovery after uninstall.' >&2
    exit 1
}
exec "$helper" install "$@"

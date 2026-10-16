#!/bin/sh
# Explicit administrator install: ./install.sh /absolute/path/minecraft-ui
set -eu
[ "$(id -u)" = 0 ] || { echo 'Run as root.' >&2; exit 1; }
[ "$#" = 1 ] || { echo 'usage: install.sh /absolute/path/minecraft-ui' >&2; exit 2; }
case "$1" in /*) ;; *) exit 2 ;; esac
[ -f "$1" ] && [ ! -L "$1" ] || exit 2
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
/usr/bin/systemctl --version >/dev/null
command -v sudo >/dev/null
if ! id minecraft-ui >/dev/null 2>&1; then
 /usr/sbin/useradd --system --user-group --home-dir /var/lib/minecraft-ui --shell /usr/sbin/nologin minecraft-ui
fi
install -d -o root -g root -m 0755 /usr/local/lib/minecraft-ui /etc/minecraft-ui
install -d -o minecraft-ui -g minecraft-ui -m 0700 /var/lib/minecraft-ui /var/lib/minecraft-ui/instances /var/lib/minecraft-ui/runtimes /var/lib/minecraft-ui/locks
# Install via rename: existing independent launchers keep their executable inode.
install -o root -g root -m 0755 "$1" /usr/local/bin/minecraft-ui.new
mv -f /usr/local/bin/minecraft-ui.new /usr/local/bin/minecraft-ui
install -o root -g root -m 0755 "$here/unitctl" /usr/local/lib/minecraft-ui/unitctl
install -o root -g root -m 0644 "$here/minecraft@.service" /etc/systemd/system/minecraft@.service
install -o root -g root -m 0644 "$here/minecraft-ui.service" /etc/systemd/system/minecraft-ui.service
/usr/sbin/visudo -cf "$here/sudoers"
install -o root -g root -m 0440 "$here/sudoers" /etc/sudoers.d/minecraft-ui
/usr/bin/systemctl daemon-reload
# Admin decides when to enable/start the manager and each configured instance.
echo 'Installed. Review /etc/minecraft-ui/environment, then systemctl enable --now minecraft-ui.service'

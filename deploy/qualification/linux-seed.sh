#!/bin/sh
# Legacy fixture setup for existing isolated service/game qualification only.
# This is not the shipping installer and does not qualify its lifecycle.
set -eu
[ "${GITHUB_ACTIONS:-}" = true ] && [ "${RUNNER_ENVIRONMENT:-}" = github-hosted ] || { echo 'Disposable hosted runner required.' >&2; exit 2; }
[ "$(id -u)" = 0 ] || { echo 'Run as root.' >&2; exit 1; }
[ "$#" = 2 ] || [ "$#" = 3 ] || { echo 'Expected ASSET_DIRECTORY MANAGER [UPDATER].' >&2; exit 2; }
case "$1" in /*) ;; *) exit 2 ;; esac
[ -d "$1" ] && [ ! -L "$1" ] || exit 2
here=$1
shift
[ "$#" = 1 ] || [ "$#" = 2 ] || { echo 'Expected MANAGER [UPDATER].' >&2; exit 2; }
case "$1" in /*) ;; *) exit 2 ;; esac
[ -f "$1" ] && [ ! -L "$1" ] || exit 2
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
if [ "$#" = 2 ]; then
 case "$2" in /*) ;; *) exit 2 ;; esac
 [ -f "$2" ] && [ ! -L "$2" ] || exit 2
 install -d -o root -g root -m 0700 /var/lib/minecraft-ui-update /var/lib/minecraft-ui-update/stages
 install -d -o minecraft-ui -g minecraft-ui -m 0700 /var/lib/minecraft-ui/selfupdate /var/lib/minecraft-ui/selfupdate/stages /var/lib/minecraft-ui/selfupdate/control
 install -o root -g root -m 0755 "$2" /usr/local/lib/minecraft-ui/selfupdate
 install -o root -g root -m 0644 "$here/minecraft-ui-update@.service" /etc/systemd/system/minecraft-ui-update@.service
 install -o root -g root -m 0644 "$here/minecraft-ui-update-recovery.service" /etc/systemd/system/minecraft-ui-update-recovery.service
 install -d -o root -g root -m 0755 /etc/systemd/system/minecraft-ui.service.d
 install -o root -g root -m 0644 "$here/minecraft-ui-update.conf" /etc/systemd/system/minecraft-ui.service.d/update.conf
 # No source/key is invented. Existing reviewed trust is preserved.
 if [ ! -e /etc/minecraft-ui/update-trust.json ]; then
  install -o root -g root -m 0644 "$here/update-trust.example.json" /etc/minecraft-ui/update-trust.json
 fi
 # The fixed manager-account capability probe has no DB/path arguments. This
 # separate root-side installer never runs the candidate as root.
 capability=$(/usr/sbin/runuser -u minecraft-ui -- /usr/local/bin/minecraft-ui update-capability)
 /usr/local/lib/minecraft-ui/selfupdate bootstrap "$capability"
fi
/usr/bin/systemctl daemon-reload
# Admin decides when to enable/start the manager and each configured instance.
echo 'Installed. Review /etc/minecraft-ui/environment, then systemctl enable --now minecraft-ui.service'

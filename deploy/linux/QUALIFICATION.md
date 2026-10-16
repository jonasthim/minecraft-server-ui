# Real systemd qualification (pending)

The restricted process namespace reports Codex as PID 1. An escalated read of
the actual host can reach systemd, but minecraft@.service is uninstalled
(LoadState=not-found). No service was installed or real unit lifecycle qualified
in this session. Package tests launch real local child processes; that evidence
does not qualify systemd behavior. Run on both supported x64 Debian and Ubuntu
hosts before marking Task 04 accepted.

Install the root-owned binary, service templates and narrow helper with
`sh deploy/linux/install.sh /absolute/path/to/minecraft-ui`. Confirm fixed paths
and the manager environment use `/var/lib/minecraft-ui`. The native supervisor
requires exactly that data root and `/usr/local/bin/minecraft-ui`; a dev manager
with another root or executable fails closed before touching the production unit
namespace. The smoke test explicitly supplies the canonical installed executable.
Root-owned nonwritable executable/helper/template and ancestors are required;
loaded unit FragmentPath, no drop-ins, exact argv, service identity, Restart=no
and KillMode=mixed must match. Unknown property formatting fails closed and must
be tested on the target systemd versions. Arbitrary dev roots or custom service
overrides are not a qualified installation contract. The game unit has no
PartOf/BindsTo relationship to the manager and has `Restart=no`; Task 19 owns
bounded recovery. The instance unit's private mount namespace exposes only its
instance, immutable runtimes and stable admission lock file. This namespace and
JVM JIT behavior require actual-host testing. Neither daemon-reload nor manager
binary replacement should restart active game units.

Prepare a dedicated disposable real Java instance using the approved managed
runtime/provider workflow. Stop it first. Do not point the smoke test at a world
that contains valuable data. Ensure its launch.json contains actual Java runtime
provenance. The persisted environment and control metadata are private and must
never be included in the report. The test preserves prior unit autostart policy
and attempts to stop its test instance during cleanup.

From the source checkout on the qualification host, explicitly opt in:

```
MINECRAFT_UI_SYSTEMD_SMOKE=1 \
MINECRAFT_UI_SYSTEMD_SMOKE_INSTANCE=supervision-smoke \
MINECRAFT_UI_SYSTEMD_SMOKE_RESTART_MANAGER=1 \
go test ./internal/supervisor -run '^TestSystemdHostLifecycle$' -v -count=1 -timeout=8m
```

The test user needs the installed narrow helper grant. The optional actual
manager restart uses systemctl directly and requires administrator authority;
do not broaden the game/manager helper to permit it. Without the restart flag,
the test proves only reconnecting a fresh supervisor object, not manager restart.

Record OS version, systemd version, binary SHA-256, managed Java/Minecraft tuple,
JVM versus launcher PID, native start/stop/status and autostart toggling, actual
manager restart with identical JVM boot/start identity, and deliberate stop with
no autonomous restart. Also perform and record:

- Boot autostart enabled and disabled instances; verify pinned versions only.
- Failed JVM unit/start and missing/inaccessible executable, preserving honest
  crashed/unknown status and no autonomous retries.
- Server ignoring stdin stop: verify bounded forced termination, exit metadata,
  service timeout and all descendants gone. Test blocked stdin/save-on retries.
- Active save lease across manager kill/restart: independent launcher must
  attempt save-on at the deadline. Never use a production world for this test.
- Concurrent configuration/deletion and an external unit start: stable
  `/var/lib/minecraft-ui/locks/<id>.lock` excludes JVM spawn during mutation.
  Lock files are retained after deletion to preserve inode identity on ID reuse.
- Manager upgrade via installed binary rename and daemon-reload while games
  run. Running launcher/JVM identities must remain unchanged. Verify rollback.
- Service mount restrictions, credentials hidden, writable instance only,
  private loopback control tokens and nonprivileged game execution.

Actual boot, service upgrade, failed-unit and forced-stop host results remain
required even if the opt-in lifecycle test passes. No real-server/client or
Minecraft version support claim follows from helper-process tests.

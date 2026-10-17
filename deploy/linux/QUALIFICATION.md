# Real systemd qualification (pending)

The restricted process namespace reports Codex as PID 1. An escalated read of
the actual host can reach systemd, but minecraft@.service is uninstalled
(LoadState=not-found). No service was installed or real unit lifecycle qualified
in this session. Package tests launch real local child processes; that evidence
does not qualify systemd behavior. Run on both supported x64 Debian and Ubuntu
hosts before marking Task 04 accepted.

For shipping first installation, independently verify the release-specific
bootstrap script and its embedded preparer hash/public key, then use
`sudo bash ./minecraft-ui-prepare.sh --version latest --install` (or pass an
exact `vMAJOR.MINOR.PATCH` release).
The legacy systemd smoke setup is not a shipping installer test; on a disposable
GitHub-hosted runner it is seeded only by `deploy/qualification/linux-seed.sh`.
Confirm fixed paths and the manager environment use `/var/lib/minecraft-ui`.
The native supervisor
requires exactly that data root and `/usr/local/bin/minecraft-ui`; a dev manager
with another root or executable fails closed before touching the production unit
namespace. The smoke test explicitly supplies the canonical installed executable.
Root-owned nonwritable executable/helper/template and ancestors are required;
loaded unit FragmentPath, no drop-ins, exact argv, service identity, Restart=no
KillMode=mixed and PrivateUsers=yes/self must match. Unknown property formatting fails closed and must
be tested on the target systemd versions. Arbitrary dev roots or custom service
overrides are not a qualified installation contract. The game unit has no
PartOf/BindsTo relationship to the manager and has `Restart=no`; Task 19 owns
bounded recovery. The game has a root-created private user namespace, zero effective capabilities
and NoNewPrivileges. The user namespace is the process capability boundary that
blocks same-account /proc/<managerPID>/{root,fd,environ} access; the mount namespace
alone does not provide that boundary. The mount namespace hides the data root on
read-only tmpfs, pins the instance parent read-only, exposes only server/logs/state
and the stable lock writable, and pins server/.manager, state/launch.json and the
runtime cache read-only. These actual mount modes and minimal UID/GID mappings
are checked before JVM exec, not inferred from unit settings. Namespace, mount
ordering and JVM JIT behavior require actual-host testing. Neither daemon-reload nor manager
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

## Private namespace inspection and immutable launch gate

The unprivileged manager deliberately cannot inspect the JVM image/cwd across
this boundary. The root-owned helper now has one additional read-only verb:
`unitctl inspect <validated-instance-id>`. It invokes only the installed binary's
hidden `native-inspect --instance ID` command. It accepts no PID, path, data root,
command or environment parameter. The broker validates the fixed loaded template,
derives MainPID/cgroup/invocation, requires exactly one direct JVM child, holds
launcher/JVM pidfds during inspection, and checks shared unit cgroup, a distinct
root-created user namespace and actual immutable/pinned mounts. It returns only
bounded identity metadata, never argv/environment/file contents. The manager
matches the full expected identity. This adds a narrow root read privilege; no
manager CAP_SYS_PTRACE is granted. Unified cgroups, pidfds and namespace-owner
inspection must work on each qualification host; absence fails closed.

Production reattachment always uses this proof, including when direct /proc
reads happen to succeed. A JVM from before PrivateUsers hardening is unknown /
repair-needed after upgrade, even if the loaded unit file has been updated. It
is not silently terminated or accepted. Arrange an explicit planned restart to
establish the new boundary. Metrics code must consume verified identities and
report any field unavailable across the namespace; the broker is not an arbitrary
process/metrics read API.

Seal and boot both rehash the managed runtime with runtime.Manager.Get, check
exact runtime ID/checksum/image/host, and offline-verify immutable provider
receipts, inventories/JAR hashes and exact argv (including loader argfiles).
Only the canonical bounded Xms/Xmx pair is optional. Arbitrary JVM flags and all
LaunchSpec.Env overrides are rejected. A future environment feature needs a
coordinated typed policy and verifier change. Inherited Java/native code-loading
variables are removed. Arbitrary helper executables exist only in private package
tests; production Run has no bypass.

Additional mandatory actual-unit evidence (not run in this session):

- From actual game code, prove /proc/<managerPID>/root cannot expose manager.db,
  another instance's state/control.json, manager fd links or environment; prove
  the same for another game's /proc root. Confirm denial rather than just a
  missing path inside the game mount namespace.
- Attempt writes, unlink and rename of launch.json, server/.manager and its
  parent, runtime files/cache parent, and instance server/state parents. All
  immutable/parent replacement attempts must fail; world/log/state updates must
  still work. Verify this using the installed unit, not a shell approximating it.
- Reattach/Inspect after manager restart through the fixed broker. Compare JVM
  boot/start/image/cwd identity; test a changed/absent namespace, mismatched
  parent/cgroup/invocation, unavailable pidfd and missing read-only mount.
- Replace only the loaded template while a disposable pre-hardening JVM remains:
  reattachment must fail closed, leave the game untouched, and require a planned
  restart. After that restart, namespace proof and console control must work.
- Tamper a disposable sealed runtime/JAR/loader argfile before boot; no JVM must
  spawn. Invalid Seal must preserve the prior activation. Confirm exact pinned
  installation/runtime under enabled boot autostart.

`systemd-analyze verify` still reports the absent installed canonical executable
on this development host. No unit installation, real JVM/EULA action, namespace
escape test, immutable rename test or native service qualification was performed.

PrivateUsers=yes preserves the host service UID. The documented boundary is
ptrace/process capabilities plus filesystem views, not distinct credentials for
each game. Ordinary same-UID signal permission remains a denial-of-service risk
to the manager/other games. No complete hostile-code sandbox claim is made; any
stronger process isolation requires a separately reviewed credential/LSM design.

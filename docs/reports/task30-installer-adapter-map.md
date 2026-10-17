# Task30 installer adapter source map

Read-only map of the current installer and self-update implementation, prepared
to freeze the narrow `internal/selfupdate` bridge contract. This is a source
map, not lifecycle acceptance evidence. No product code, accounts, services,
hosts or game processes were changed.

## Assets and native registrations shipped today

| Platform | Fixed destination | Source / conditions | Identity and behavior |
| --- | --- | --- | --- |
| Linux | `/usr/local/bin/minecraft-ui` | supplied manager binary, atomically installed through `.new` then rename | root:root `0755`; manager and game launchers use this stable path |
| Linux | `/usr/local/lib/minecraft-ui/unitctl` | `deploy/linux/unitctl` | root:root `0755`; fixed systemctl wrapper for instance start/stop/enable/disable/inspect |
| Linux | `/etc/systemd/system/minecraft-ui.service` | `deploy/linux/minecraft-ui.service` | manager runs as `minecraft-ui:minecraft-ui`; `serve`; `MINECRAFT_UI_DATA_DIR=/var/lib/minecraft-ui`, loopback `127.0.0.1:8080`, optional `/etc/minecraft-ui/environment`; restart on failure |
| Linux | `/etc/systemd/system/minecraft@.service` | `deploy/linux/minecraft@.service` | per-instance launcher runs as manager account; game unit is intentionally independent of manager restart and has `Restart=no` |
| Linux | `/etc/sudoers.d/minecraft-ui` | `deploy/linux/sudoers`, checked with `visudo` | root:root `0440`; manager can call only the fixed `unitctl`, plus optional self-update `status` and `request *` verbs |
| Linux, updater option | `/usr/local/lib/minecraft-ui/selfupdate` | optional second installer argument | root:root `0755`; canonical updater executable |
| Linux, updater option | `/etc/systemd/system/minecraft-ui-update@.service`, `minecraft-ui-update-recovery.service`, manager drop-in `minecraft-ui.service.d/update.conf` | shipped updater unit files | root one-shot worker for activation; recovery service runs before manager; updater units retain `CAP_SETUID` for fixed manager-account probes; optional updater install creates update state and manager control/staging directories |
| Linux, updater option | `/etc/minecraft-ui/update-trust.json` | `update-trust.example.json`, only if destination is absent | root:root `0644`; existing trust is preserved |
| Windows | `C:\Program Files\MinecraftUI\minecraft-ui.exe` | supplied manager binary | executable directory ACL: Administrators/System full control, manager SID read/execute; service runs `serve` |
| Windows | `C:\ProgramData\MinecraftUI` | created data root | manager service environment sets this data path and `127.0.0.1:8080`; service account receives Modify ACL |
| Windows | SCM service `MinecraftUI` | `New-Service -Credential` | dedicated unprivileged manager identity, automatic startup; install script intentionally leaves it stopped |
| Windows, updater option | `C:\Program Files\MinecraftUI\selfupdate.exe`, `minecraft-ui-probe.exe`, `update-trust.json` | updater binary, manager copy, empty trust if missing | Program Files remains non-writable by manager. Probe image is the manager binary and is run under the same manager credential. Trust is protected with the executable directory. |
| Windows, updater option | SCM service `MinecraftUIUpdateProbe` | fixed image `minecraft-ui-probe.exe update-schema-service` | manual demand-start, manager credentials; reads schema and actor authorization or candidate capability through fixed control files |
| Windows, updater option | SCM service `MinecraftUIUpdater` | fixed image `selfupdate.exe service` | automatic LocalSystem service; manager depends on it so interrupted executable recovery completes before manager start |
| Windows, updater option | `C:\ProgramData\MinecraftUIUpdate` and `C:\ProgramData\MinecraftUI\selfupdate\control` | bootstrap/updater setup | protected updater state and manager-writable bounded control area; `account.json` binds updater pipe access to the manager SID |

Both installers currently reject rerunning over an existing installation: Linux
does direct copy/rename operations without inventory or rollback; Windows
rejects an existing manager service. Linux installs manager/helper files before
bootstrap can fail. Windows creates directories and copies before all service
configuration has completed. Those are the integration gaps the lifecycle
transaction must close.

## Existing self-update capabilities to wrap

`internal/selfupdate/native.go` defines a private `nativeDriver` that already
groups fixed native operations: `Stop`, `Start`, `ProbeSchema`,
`ProbeCandidate`, `WaitHealthy`, `WriteChallenge`, `ReadDrain`, `ClearControl`,
`LockManager`, and `Fence`. The platform implementations are unexported, but
they are the narrow implementation seam for a new exported maintenance facade;
the installer should not execute systemctl/SCM commands itself.

Reusable behavior:

- The privileged update operation lock is `launcher.AcquireManager(paths.State)`;
  the manager data lock is `launcher.AcquireManager(paths.Data)`. Neither lock
  file is removed. `AcquireManager` currently calls `MkdirAll`, so maintenance
  must first verify canonical roots exist and are safe; it must not let a lock
  helper silently create state during a read-only check.
- Linux `linuxDriver.Stop` stops only `minecraft-ui.service`, verifies systemd
  `MainPID=0`, and has a 155-second bound. `Start` delegates to the existing
  lock-owner-bound launch permit. The `minecraft@*.service` game units are
  untouched.
- Windows `windowsDriver.Stop` opens the fixed `MinecraftUI` service, rejects
  nonempty automatic SCM recovery actions, sends graceful stop, and waits for
  `Stopped`. It does not force-kill. `Start` opens and starts that same fixed
  service.
- `WriteChallenge` uses a fresh nonce, expires it, removes stale fixed control
  files, and writes only to the canonical `selfupdate/control` directory.
  `PrepareDrainReceipt` is called by the manager after HTTP/jobs/save leases and
  transactions drain; it checks unfinished DB work and per-instance markers.
  `PublishDrainReceipt` runs after DB close, before releasing the manager lock.
  `ReadDrain` validates the bounded receipt, and the activation worker compares
  nonce, version, schema and executable SHA-256.
- `LockManager` polls the real manager data lock. The activation worker only
  replaces after it owns that lock and has verified the drain receipt.
- Current-schema probing runs the fixed installed manager as the manager
  account. Linux uses `/usr/sbin/runuser` and the fixed `update-schema` verb;
  Windows starts the demand probe service and validates the fixed SCM image,
  service account SID and result file. Candidate probing uses the fixed `.next`
  image on Linux or fixed probe image on Windows, with expected version/schema/
  hash and no DB mutation.
- `WaitHealthy` is bounded to 90 seconds, proves the running image, then polls
  the private loopback readiness endpoint with a one-use nonce and checks
  version/schema/executable hash. Linux verifies systemd MainPID points to the
  installed executable inode; Windows verifies service image identity.
- `Fence` provides the existing platform durability barrier. Receipt operations
  (`readInstalled`, `recordInstalled`) and protected roots are also private
  selfupdate implementation and should be exposed only as typed compare-and-
  swap operations.

## Actor boundary and minimum missing primitive

The current challenge is explicitly web-actor-bound. `Challenge.ActorID` is
required by the worker journal and current-schema probe: `update-schema` reads
the live DB and requires that actor still be an enabled administrator. The
candidate probe is a different fixed request and skips that authorization
query. A local OS administrator must not be represented by a fake `ActorID`,
and the installer must not call `ActivationRequest` or reuse
`activationJournal` for helper/service/schema-changing transactions.

The minimum new primitive is a separate local-maintenance drain/probe flow in
`internal/selfupdate`, with authority checked by the native process token before
any filesystem or lock I/O. It should bind a fresh nonce to a bounded fixed
maintenance operation ID and expected manager image, request the same graceful
manager drain, wait for process exit and the real manager lock, validate the
matching drain receipt, and perform a manager-account read-only schema probe
that returns image identity without querying a web actor. It must refuse when
the manager is alive but does not produce a complete matching drain proof; an
already-stopped manager can instead use the existing stopped-state lock and
read-only probe path without fabricating a drain receipt.

Suggested bounded facade (concrete structs belong in root's contract patch):

```go
type MaintenanceImage struct {
    Version string
    Schema  int64
    SHA256  string
}
type MaintenanceProof struct {
    Image MaintenanceImage
    Drained bool
    Stopped bool
    Nonce string // fixed-format random nonce; never exposed in public output
}

func BeginLocalMaintenance(ctx context.Context) (*MaintenanceSession, error)
func (s *MaintenanceSession) Inspect(ctx context.Context) (MaintenanceState, error)
func (s *MaintenanceSession) StopAndDrain(ctx context.Context, prior MaintenanceImage) (MaintenanceProof, error)
func (s *MaintenanceSession) ProbeCandidate(ctx context.Context, image MaintenanceImage) (MaintenanceImage, error)
func (s *MaintenanceSession) StartAndWait(ctx context.Context, image MaintenanceImage, proof MaintenanceProof) error
func (s *MaintenanceSession) CompareAndSwapInstalled(ctx context.Context, prior, next MaintenanceImage) error
func (s *MaintenanceSession) Close() error
```

`BeginLocalMaintenance` must do the platform administrator/root check before
opening protected roots, then acquire the existing privileged update lock and
validate the fixed installation. The session owns that lock for its lifetime.
It may acquire the manager data lock only after stop/drain and must release it
before `StartAndWait` or any child probe which needs that lock. The operation
lock and startup fence remain held across the release/reacquire boundary.
Caller-facing results must omit the nonce and all paths/raw errors.

The smallest protocol change is an installer-specific challenge/result type
and control filenames, rather than making `ActorID` optional in the existing
activation protocol. Share only the drain logic that proves manager work is
quiescent; keep its web-update authorization check and existing wire format
unchanged. Likewise, add a fixed capability-only current-image probe rather
than weakening `ReadOnlyActorAuthorized`. Both modes still run only the
installed manager as its configured unprivileged account.

## Dependency and contract constraints

- `internal/selfupdate` must not import `internal/install`: both candidate and
  bootstrap already import selfupdate, so that direction would cycle. The
  lifecycle engine can import selfupdate and use the narrow facade.
- Keep `nativePaths`, native drivers and system service names private to
  selfupdate. Expose typed fixed-operation methods, not paths, commands,
  arbitrary service names, actor IDs, or caller-selected executables.
- Preserve the existing activation journal, updater pipe grammar, sudoers
  grammar and manager web-actor validation. Local admin authority is a separate
  native-token check.
- Freeze lock order as privileged update lock → manager graceful drain/exit →
  data lock. Do not hold the data lock while starting manager/probe children.
- An installer-owned start needs its own durable transaction/permit proof and
  startup recovery fence. Existing Linux launch permit helpers are private and
  tied to `activationJournal`; share process/lock identity internals only after
  root defines a separate typed permit, never by forging an activation journal.
- Windows self-update's LocalSystem updater and manager-SID probe are useful
  execution boundaries. They do not alone establish local administrator
  authority for installer mutation; the new command must validate the caller's
  actual elevated token before mutation and wait for manager, updater and probe
  image handles before replacing their files.

Source review only: no service invocation, account operation, filesystem
mutation outside this report, or native qualification was performed.

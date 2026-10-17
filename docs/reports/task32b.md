# Task32B — installed native updater acceptance harness

Status: source candidate prepared for integrator review. **Installed updater
qualification remains pending.** No native service/account installation or
acceptance execution took place on the development host. Task28's installed
manager evidence and Task29's artifact evidence retain their separate scopes.

## Source and build contract

The root owns Actions wiring and any shipping installer/native corrections.
This packet owns only the new `cmd/qualify-selfupdate/**`,
`deploy/qualification/*updater*`, and this report. Existing shipping verifier,
Stager, API, native drivers, installers, trust defaults and release gate are
unchanged by this packet.

Root's workflow builds these inputs on actual Ubuntu24.04 and Windows2022
hosts, with the same embedded frontend/schema and source revision:

```sh
go build -trimpath -ldflags '-X main.version=v0.0.1' -o manager-initial ./cmd/minecraft-ui
go build -trimpath -ldflags '-X main.version=v0.0.2' -o manager-success ./cmd/minecraft-ui
go build -trimpath -ldflags '-X main.version=v0.0.2' -o updater ./cmd/minecraft-ui-update
go build -trimpath -o qualifier ./cmd/qualify-selfupdate
go build -trimpath -o manager-negative ./cmd/qualify-selfupdate/negative
```

Windows outputs have `.exe` suffixes. The negative fixture has an explicitly
fixed `v0.0.3` identity and the actual `db.LatestVersion()` schema. Build input
artifacts are exchanged between jobs; both actual platform binaries appear in
the signed two-platform fixture manifests. A qualification job executes only
its native binaries. No production artifact gains another member.

Entrypoints, with an existing caller-owned report directory:

```sh
sudo --preserve-env=GITHUB_ACTIONS,RUNNER_ENVIRONMENT bash deploy/qualification/linux-updater.sh \
  INITIAL SUCCESS UPDATER QUALIFIER NEGATIVE PEER_SUCCESS PEER_NEGATIVE REPORT_DIR
```

```powershell
./deploy/qualification/windows-updater.ps1 `
  -InitialBinary INITIAL -SuccessBinary SUCCESS -UpdaterBinary UPDATER `
  -QualifierBinary QUALIFIER -NegativeBinary NEGATIVE `
  -PeerSuccessBinary PEER_SUCCESS -PeerNegativeBinary PEER_NEGATIVE `
  -OutputDirectory REPORT_DIR
```

The scripts resolve inputs, require GitHub's disposable hosted-runner markers
and native root/Administrator context, and refuse existing canonical
installations, services, state or service identities. They invoke the current
shipping installer with the initial genuine manager and updater, including
Linux recovery units or Windows updater/probe services.

## Acquisition boundary

`qualifier prepare` generates an ephemeral Ed25519 signing key in memory and
clears its private-key byte slice on return. It writes only public fixture
trust, signed manifests/signatures and exact three-member archives containing
the actual candidate executable, repository LICENSE and notices. The paired
platform metadata describes actual exchanged build bytes. This is explicitly
**injected artifact acquisition**, not GitHub discovery, public downloading,
production signing or release publication.

Public fixture trust is copied to the native root/admin protected configuration
only on the disposable host. The independent updater still loads and verifies
that configuration through its shipping trust reader. The staging helper runs
under the dedicated manager account and calls the unchanged `selfupdate.Stager`.
The API receives the returned stage identifier through the real authenticated
administrator `POST /api/v1/system/update/apply` endpoint. The helper does not
write native activation journals/receipts or replace installed executables.

The test trust and archives are private scratch/native test state, removed on
cleanup and excluded from reports. No private key is written to disk or passed
through arguments, environment or diagnostic output.

## Installed cases prepared for hosted execution

1. **Genuine success:** start actual installed `v0.0.1`, use real empty-database
   setup to create an administrator/session, and activate actual `v0.0.2`.
   Require native terminal `healthy`, exact installed receipt/schema/hash,
   actual SCM/systemd PID bound to the expected executable and dedicated
   identity, and no old/duplicate manager. The shipping worker proves drain,
   current administrator authorization, manager-account schema/capability,
   executable identity and private readiness before writing `healthy`.
2. **Negative health rollback:** activate the explicitly signed `v0.0.3` test
   candidate. It has real fixed capability/schema checks and, on Windows,
   invokes the shipping `WriteSchemaProbeResult` through the actual configured
   manager-account probe service. Its normal service runs under SCM/systemd
   but deliberately opens no HTTP listener. Require the actual negative
   service PID/image before waiting for the shipping 90-second readiness
   failure. Require native `rolled_back`, `rollback_attempted=true`, healthy
   genuine `v0.0.2` restored with exact prior bytes and receipt, and the
   negative PID gone. A probe rejection, aborted update or failed startup
   cannot accidentally pass this case.
3. **Interrupted candidate recovery:** issue a separate API activation of the
   negative fixture, observe protected journal phase `candidate` and the
   actual negative service image/PID. Normal service startup occurs after the
   candidate journal and replacement durability fences, so this observation
   identifies a concrete durable boundary. Kill only the independently bound
   updater service/operation with SIGKILL or `Stop-Process -Force`, verify the
   journal remains `candidate` without rollback, and stop the negative service.
   Invoke normal shipping recovery through the manager's systemd dependency
   or restarted Windows updater service, then start the manager. Require
   `rolled_back`, prior exact bytes/version/schema and healthy service.
   No journal edit, synthetic recovery fixture or production pause hook is
   involved. The unhealthy fixture provides the bounded 90-second observation
   window; absence of the observed boundary fails the test.

Each case checks the original administrator session and fresh login, unchanged
SQLite file identity, schema, and an exact application-data sentinel digest.
Control/challenge/drain state must be reconciled. The final native stop must
leave no manager process. Linux also keeps a clearly labelled independent
dummy sleep service alive with the same PID through all three cases. That
asserts native lifetime mechanics only; it is not Minecraft/game evidence.
Installed manager/game unit files retain their shipping contents and manager
sandbox properties are checked explicitly.

Windows uses a freshly generated dedicated local account. Passwords remain
inside the in-memory SecureString/PSCredential and Windows logon/SCM, never
arguments, environment, policy exports or plaintext files. The harness grants
only `SeServiceLogonRight` for that SID through LSA and removes that grant and
account afterwards. It verifies actual manager/updater process owner SIDs,
SCM bindings, probe account SID, private updater-state ACL/owner, executable RX
and manager-data Modify rights. The unchanged shipping driver performs the
unprivileged schema/probe and pipe-client identity checks.

## Reports, failure and cleanup

Caller report directory files are fixed-field evidence only:

- `acceptance-summary.json`: exact terminal phase/version/schema/hash, native
  process identity, prior PID, rollback status and preservation assertions.
- `interruption-boundary.json`: observed candidate phase/hash/process and
  independently verified updater worker PID/identity; excludes the nonce.
- `api-assertions.txt`, input hash/version summaries and native identity/unit/
  SCM summaries; journal/events are reduced to fixed identifiers and counts.
- `result.json`: truthful exit/failure and cleanup state, including failures
  before or during installation/acceptance.

No DB, cookie jar, session/token, administrator password, configuration,
private key, challenge nonce, raw event message or raw journal is retained.
Cleanup stops/removes only canonical services/files/accounts that this fresh
run was permitted to create, checks remaining processes/state, and reports
cleanup failure as a failed run. No installer/service command has been run on
the development machine. The harness never starts Minecraft or accepts EULA.

## Local evidence and remaining gates

Verified locally on 2026-10-17:

- `go test -race ./cmd/qualify-selfupdate/... ./internal/selfupdate` passes.
  Acquisition-helper tests use isolated temporary files, exercise both ZIP
  and tar.gz through the actual verifier/Stager, reject archive corruption and
  signed-metadata tampering, refuse existing fixture output, and check the
  exact allowed fixture files/public-key shape. These tests are not installed
  acceptance or interruption evidence.
- `go vet ./cmd/qualify-selfupdate/...` passes.
- New helper/negative commands compile for Linux and Windows with
  `CGO_ENABLED=0 GOARCH=amd64`; the Windows build is compilation evidence only.
- Bash syntax, Python import/3.10 grammar, multi-chunk exact SHA256 fixture and
  non-hosted Linux refusal checks pass. `git diff --check` passes.

PowerShell is unavailable in the development environment; parsing and execution
of the new scripts remain required on Windows2022. No installed updater case
has yet run on Ubuntu22.04/24.04 or Windows2022. No shipping blocker has been
demonstrated by this source-only packet. Any actual hosted installer/native
failure must be reported with its narrow proposed product fix to the root
before changing shipping code. Root must review the source and actual hosted
artifacts before accepting Task32B or updating the implementation ledger.

This evidence does not qualify games, real clients, backup/restore, other OS
versions, production trust/signing/publication, or a stable release.

## Review correction R1 — installer scratch coverage

Independent source review of `8f40508..3b5b629` identified an omitted fixed
installer path: `/usr/local/bin/minecraft-ui.new`. The Linux harness now includes
it in the fresh-state inventory, which also drives final absence checks, and
in the explicit owned executable cleanup command. Refusal still occurs before
`owned=1`, so a preexisting regular file or dangling symlink is neither claimed
nor removed. No shipping installer/native product code changed.

Narrow safe regression: `python3 deploy/qualification/updater_paths_test.py -v`
passes all three tests. It extracts only the static inventory/refusal/final
check and fixed executable cleanup operands, maps every path into temporary
fixture directories, and verifies regular-file/symlink refusal with preserved
contents, detection/removal of owned partial installer output, and empty-state
acceptance. It never executes the installer or service/account operations and
never writes canonical host paths.

Before the fix, the regular/symlink refusal cases and final absence check failed.
With only the inventory corrected, refusal/final detection passed and cleanup
still failed because `.new` survived. With the removal list corrected, all
three tests pass (`Ran 3 tests ... OK`). `bash -n
deploy/qualification/linux-updater.sh` and scoped whitespace checks also pass.
No broad unchanged suites or native installations were repeated. Hosted updater
qualification remains pending integrator review and execution.

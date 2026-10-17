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

## First hosted updater run — actual failure, 2026-10-17

[Run39030086733](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39030086733)
at `33c256a1c685e0b24f525309cdd9d09fc4697cfa` (embedded schema19) passed both native
input-build jobs, then **failed all three installed jobs at shipping installer
bootstrap, before activation**. The source approval and local fixture results
above do not override this failed native result.

| Actual runner | Image version | Result before activation |
| --- | --- | --- |
| Ubuntu22.04/systemd249.11-0ubuntu3.22 | `ubuntu22 20261012.322.1` | Bootstrap refused: `native updater directory is not root-owned and protected` |
| Ubuntu24.04/systemd255.4-1ubuntu8.17 | `ubuntu24 20261012.334.1` | Same protected-directory refusal |
| WindowsServer2022 | `win22 20261012.333.1` | Bootstrap refused: `native update path is writable by an untrusted principal` |

The scrubbed artifacts were inspected read-only using `gh run download`.
All three retain failed results with cleanup checked; there are no successful
activation summaries. Windows records only `MinecraftUI`, stopped with PID0
and SCM exit1077 (not started); updater/probe creation and administrator setup
were not reached. No game/EULA/release action occurred.

### Linux cause and explicit hosted-image provisioning

The Linux bootstrap opens protected state first and then the fixed executable
directory `/usr/local/bin`. The matching official image build sources explicitly
execute `sudo chmod -R 777 /usr/local/bin`:

- Ubuntu22 tag `ubuntu22/20261012.322`, source commit
  `f4ad24e10727c119f9f86675a60808ccc241774d`:
  [install-nodejs.sh](https://github.com/actions/runner-images/blob/f4ad24e10727c119f9f86675a60808ccc241774d/images/ubuntu/scripts/build/install-nodejs.sh).
- Ubuntu24 tag `ubuntu24/20261012.334`, source commit
  `7e7dc1bcc0ef464e5e315a2b4c33609590c6cffe`:
  [install-nodejs.sh](https://github.com/actions/runner-images/blob/7e7dc1bcc0ef464e5e315a2b4c33609590c6cffe/images/ubuntu/scripts/build/install-nodejs.sh).

The installer protects its own executable but does not change that shared
directory. A writable executable parent is unsafe: another account could
replace directory entries despite the installed file's root ownership. The
shipping `protectedInfo` rejection must remain intact. The first run did not
capture the rejected path's actual metadata; the exact matching image sources
identify `/usr/local/bin` as the expected rejecting prefix, and new fixed-path
diagnostics will verify the actual before/after metadata on the next run.

Root explicitly authorized this narrow disposable-host provisioning exception:
after hosted/root/fresh-install checks and before invoking the shipping
installer, verify real root-owned `/`, `/usr`, `/usr/local`, `/usr/local/bin`
ancestry (other ancestors must already be protected), record original
owner/group/mode and directory identity, and remove only group/other write bits
from `/usr/local/bin`. Preserve all other permission bits. Cleanup restores the
exact original mode only if directory identity/owner/group remain unchanged,
asserts restoration, and reports failure if restoration cannot be verified.
This borrowed-directory mode change is documented separately from cleanup of
owned application state. No unrelated owner/group/ACL is changed.

The result will therefore qualify installation **after explicit hosted-image
prefix provisioning**, not out-of-box self-update on a writable prefix. No
shipping installer or native protection guard has been modified.

### Windows diagnostic gap — no product correction yet

Windows bootstrap first calls `openProtectedRoot` on
`C:\ProgramData\MinecraftUIUpdate`, walking through `C:\ProgramData` and `C:\`
before validating `bootstrap-capability.json`; it later checks the executable
ancestry. The initial artifacts contain no path/DACL metadata, so the exact
rejecting path/principal/mask is **not yet established**. The preceding
`bootstrap-capability.json` ownership log does not prove that file caused the
refusal.

The shipping guard treats rights bit2 as a forbidden write on every path. For a
directory, bit2 means create-file; existing-child replacement and delete-child
rights are separate. A standard create-only ancestor ACE is a plausible
explanation, pending actual evidence. See Microsoft's
[file access constants](https://learn.microsoft.com/en-us/windows/win32/fileio/file-access-rights-constants)
and [file security semantics](https://learn.microsoft.com/en-us/windows/win32/fileio/file-security-and-access-rights).
No Windows product fix or ancestor permission change is authorized or made.
Any later correction must retain protected leaf/file checks and existing
replacement, deletion, DACL, owner, reparse and binding defenses.

Both owned harnesses now emit metadata-only permission reports for fixed native
paths/ancestors, including a failure snapshot before cleanup. Linux emits
`permission-summary-linux.json` with UID/GID/mode/type; Windows emits
`permission-summary-windows.json` with owner/principal SIDs, numeric rights,
allow/deny type, inheritance/propagation flags and reparse status. No file
contents, SDDL, credentials or arbitrary input paths are included. These files
match the existing scrubbed artifact uploader's JSON allowlist.

### Narrow local verification of the diagnostic/provisioning packet

`python3 -B deploy/qualification/updater_paths_test.py -v` passes all six tests.
The new tests remap the mode lifecycle to temporary directories: `0777`,
`02777`, `01777`, and `0755` lose only group/other write bits and return to
their exact original modes; a changed/symlink directory binding refuses
restoration without touching its target. A temporary diagnostic fixture proves
that only allowlisted metadata is emitted, never file contents. No canonical
host path, service or account is changed by these tests.

Bash syntax, Python3.10 grammar and scoped whitespace checks pass. New Windows
PowerShell diagnostics still require actual hosted parsing/execution. The
diagnostic rerun remains an integrator dispatch/review gate; no successful
installed updater evidence has yet been obtained.

## Diagnostic rerun39033415308 — exact observed blockers

[Run39033415308](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39033415308)
at `23698965ac067a5a0e22decf6a8157a42971438e` completed **failure**: both native
build jobs passed; Ubuntu22, Ubuntu24 and Windows2022 installed jobs failed.
Scrubbed reports were downloaded read-only to a separate run-specific cache.

The Ubuntu image versions remain `ubuntu22 20261012.322.1` and
`ubuntu24 20261012.334.1`. Both now directly record the same real, root-owned
`/usr/local/bin` lifecycle:

| Snapshot phase | UID:GID | Mode | Symlink |
| --- | --- | --- | --- |
| Before hosted-prefix provisioning | `0:0` | `0777` | false |
| After provisioning | `0:0` | `0755` | false |
| Before application cleanup | `0:0` | `0755` | false |
| After exact prefix restoration | `0:0` | `0777` | false |

The shipping installer/bootstrap now succeeds on both Ubuntu runners. The
harness reaches manager-account staging, actual native manager startup, empty
database setup, authenticated administrator/session and update availability
checks. Manager unit summaries show the genuine unprivileged manager running
with the shipping sandbox. The **first activation POST returns HTTP403**, so
no native executable activation or recovery case has passed. Source trace
identifies a harness omission: `manager_api.request` sets JSON content type but
no `X-CSRF-Token`; shipping authenticated cookie mutations require the
session-bound token returned by `GET /api/v1/auth/me`. The narrow correction is
to supply that in-memory token for the updater harness's activation requests,
retaining all product CSRF checks and avoiding another package's request helper.
Both failed run results still record cleanup checked and the actual prefix
restoration above.

Windows remains `win22 20261012.333.1` and fails during shipping installer
bootstrap. The `failure-before-cleanup` DACL snapshot establishes the exact
first rejected path in the shipping ancestry walk:

- `C:\ProgramData\MinecraftUIUpdate` passes: administrator owner, no reparse,
  effective ACEs only for SYSTEM and Administrators, both full control.
- Next, `C:\ProgramData` has owner SYSTEM, no reparse, and an effective explicit
  allow ACE at index4 for Users (`S-1-5-32-545`) with mask278 (`0x116`),
  ContainerInherit1, propagation flags0 (not inherit-only).
- That ACE intersects the current forbidden mask `0x500d0042` only at `0x2`.
  Its full mask is `FILE_ADD_FILE(0x2) | FILE_ADD_SUBDIRECTORY(0x4) |
  FILE_WRITE_EA(0x10) | FILE_WRITE_ATTRIBUTES(0x100)`. It grants no DELETE,
  DELETE_CHILD, WRITE_DAC, WRITE_OWNER, GENERIC_WRITE or GENERIC_ALL.
- The bootstrap file itself is administrator-owned and inherits only SYSTEM
  and Administrators full control. The guard fails at the ancestor before
  reaching that file; the bootstrap file is not the cause.

This confirms the earlier create-only ancestor hypothesis. Proposed narrow
product correction: distinguish inspection of an **existing ancestor
directory** from a protected application leaf directory/file, so the ancestor's
create-new-child permission is not mistaken for write access to an existing
protected child. Preserve strict leaf/file checks and ancestor owner, delete/
replacement, DACL/owner-change, reparse and binding defenses; do not modify the
system ProgramData ACL or trust an unprotected application directory. Root
assignment and a focused regression/review are required before a shipping edit.

The new Windows diagnostic PowerShell actually parsed and ran on the native
host, but installed updater activation remains unqualified on all three
targets. This run proves Linux provisioning/restoration and identifies the next
two blockers; it does not prove successful executable replacement or recovery.

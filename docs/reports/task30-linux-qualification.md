# Task 30 Linux installer qualification harness

## Deliverable

Added a workflow-dispatch-only native qualification pipeline. Linux and Windows x64 build jobs stamp manager, updater, and bootstrap binaries for `v0.0.1` through `v0.0.3`, execute each binary on its native hosted runner, and retain byte-bound execution receipts. The Linux `v0.0.3` manager is built with an overlay that changes only the `serve` command to wait for cancellation before readiness. Version and capability probes remain the shipping implementation. Workflow metadata explicitly labels this as a modified unhealthy fixture that must not be released.

The Go fixture producer uses the existing candidate producer with one in-memory Ed25519 development key for all three versions. It verifies all six platforms/versions' native execution receipts, publishes new output only, verifies that the public development trust is identical across candidates, and never writes the private key. These candidates are fixtures and are not published as releases or production trust.

The root-only shell harness refuses non-GitHub-hosted runners, existing canonical files, units, accounts, or active Minecraft template instances. It temporarily removes group/world write bits from borrowed `/usr/local/bin` only after recording its root-owned device/inode/uid/gid and mode, then restores that exact mode only if identity still matches. Its Python runner invokes the shipping bootstrap and installer commands for absent/read-only check, tampered metadata refusal, fresh install, unchanged rerun, v2 upgrade with a running fixture unit, unhealthy v3 rollback, killed-worker recovery and startup refusal, uninstall refusal/retention/idempotence/reinstall, and a separate failed-first-install retained-data retry. The process-lifetime fixture is `minecraft@installer-fixture.service` running `/usr/bin/sleep`; it is explicitly not Minecraft evidence. Uninstall refusal also proves the manager process identity stays unchanged while the fixture game unit runs. Successful uninstall runs with the manager active after the game fixture stops; the harness checkpoints it with a normal stop, records database inode/schema/migration/user state read-only, restarts it, and verifies retained database identity and logical state after shipping uninstall. Reports contain bounded lifecycle summaries only. Failure cleanup first kills only exact installer operation units, gives the manager 180 seconds to stop, verifies workers and manager have no process before removing state, and removes the fixture account only when its home and shell match exactly. A diagnostic artifact contains fixed service properties and journal event counts classified into a small stage allowlist; raw journal messages are discarded.

A third matrix scenario exercises coexistence with the ordinary self-updater. It uses the existing `cmd/qualify-selfupdate` producer/stager and `updater_api.py` acceptance helper with Linux and Windows native v0.0.2/v0.0.3 peers. It installs v0.0.1, adds a fixed root-owned mode-0644 operator trust file only after recording that installer defaults left it absent, runs actual authenticated API setup and updater success/rollback/interruption recovery, and checks that installer `check` recognizes the updater's terminal v0.0.2 receipt and executable hash. It then applies signed installer v0.0.2, checks manager health and retained markers/trust/fixture-unit identity, and performs shipping uninstall while retaining the manager DB and updater data. A separate `minecraft-ui-qualification-independent.service` running `/usr/bin/sleep` is the exact preflighted and cleaned process-lifetime fixture for this scenario. The harness retains only the updater helper's bounded sanitized evidence under `updater-*` filenames. This is updater coexistence evidence and makes no Minecraft support claim.

## Local verification

The following pure checks passed:

- `python3 -m py_compile .github/scripts/linux-installer-failure-overlay.py .github/scripts/linux-installer-failure-overlay_test.py deploy/qualification/linux_installer.py deploy/qualification/linux_installer_test.py deploy/qualification/linux_installer_diagnostics.py deploy/qualification/linux_installer_diagnostics_test.py`
- `python3 .github/scripts/linux-installer-failure-overlay_test.py` — 2 tests passed
- `python3 deploy/qualification/linux_installer_test.py` — 12 tests passed after adding the bounded command-failure diagnostic coverage
- `python3 deploy/qualification/linux_installer_diagnostics_test.py` — 1 scrubber test passed
- `/home/thim/.local/go/bin/go test ./cmd/qualify-installer`
- `/home/thim/.local/go/bin/go vet ./cmd/qualify-installer`
- `GOFLAGS=-buildvcs=false /home/thim/.local/go/bin/go test ./release/candidate`
- `/home/thim/.local/go/bin/go test ./cmd/qualify-installer` after adding the Windows `.exe` path regression
- `bash -n deploy/qualification/linux-installer.sh`
- Python YAML parse of `.github/workflows/linux-installer.yml`
- `git diff --check` for assigned files

## First dispatched run

Workflow run `39060088701` passed both native input build and upload jobs, then
failed in `Produce ephemeral signed Linux installer fixtures` before any
installer lifecycle job ran. The failure was a producer path mismatch: the
workflow builds Windows inputs as `manager.exe`, `updater.exe`, and
`bootstrap.exe`, while the producer expected extensionless filenames. The
producer now appends `.exe` for Windows input paths. Tests cover acceptance of
the workflow-shaped Windows receipt layout and refusal when only an
extensionless substitute exists.

The integrator replayed the complete corrected producer with both native input
artifacts from that run. It successfully produced all three authenticated
disposable candidates locally. This replay changes only temporary package
outputs and supplies no installed-service acceptance evidence.

No host service, account, firewall, manager, updater, or game command was run
locally. The first dispatched run did not execute native installer lifecycle
tests and therefore makes no Ubuntu acceptance claim. The fixture unit and
marker are only process-lifetime and persistence checks, not a Minecraft
support claim. The ordinary updater coexistence case is configured as its own
native matrix scenario; the first run did not reach it.

## Follow-up run

Workflow run `39060449871` passed native input builds, artifact uploads, and
signed fixture production, then failed all six lifecycle matrix jobs. Bounded
summaries show the common earliest lifecycle and retained-retry failure was the
first valid `prepare --install`: it returned `manual_repair` at stage `prepare`
with an operation ID. The scrubbed service/journal diagnostic had no journal
events and both fixed services inactive. The run also exposed that artifact
extraction loses executable bits on manager peers used by the strict
updater-coexistence preflight. The workflow now restores expected Linux and
Windows manager modes only for that scenario; the preflight remains strict.
No successful native installer lifecycle run has completed yet.

Source diagnosis localized the first-install refusal to executor staging under
the harness's restrictive umask077. The installer checked0755 before setting
that mode. A subprocess regression reproduced the refusal, and descriptor-based
mode setting now passes. Follow-up review also corrected new deployment parent
directories retaining0700, which prevented the service account from reading
operator updater trust. New directories receive0755; borrowed directories keep
their metadata. Both fixes passed independent source review, focused repeated
regressions and the integrator's combined race/vet checks. Hosted rerun remains
required.

## Worker startup investigation

Run `39061276106` at `08d7d16` passed native builds and signing. All six scenarios
passed absent/read-only/authentication refusal, then reached worker dispatch and
returned `pending`. The worker journal contained a manual-repair result, but the
original scrubber omitted its failed stage; no protected final result or manager
startup was observed. This is not lifecycle acceptance.

A temporary process fixture successfully stages and records an executor, closes
the parent, executes that exact file, and loads/verifies it through a fresh
driver. Actual hosted session/systemd behavior remains under investigation.
Diagnostics now retain only strict validated worker results bound to their unit
ID and four exact authored entry-failure categories. Unknown fields, duplicate
JSON, near-match messages and malformed journal objects are rejected. Mixed
optional result fields sort safely. Nine diagnostic and twelve harness checks
pass; the integrator's focused staged-executable race regression also passes.

Run `39061902567` supplied the missing worker result: every scenario failed at
`publish`. The integrator reproduced a fresh-worker snapshot gap: `Replace`
skipped unchanged roles before retaining their handles, so `Publish` could not
validate absent optional update trust. Every role is now inspected against its
exact captured preimage and retained before an unchanged-content skip. The
handoff composition regression failed before this change and passes afterward;
package race/vet and independent native review also pass. A hosted rerun is
required to establish the correction's effect on the full lifecycle.

## First healthy installed manager

Run `39063841719` at `99ade99405a97eed6f2741f46fe101150a010f02` passed both native input builds and signing. In lifecycle and updater-coexistence on Ubuntu22.04/24.04, the worker reported `installed` for v0.0.1 and the service was active with a nonzero main PID. This confirms first installation progressed past publication and startup.

All six scenario jobs still failed. Lifecycle retained a successful last command without its check report; coexistence retained exit1 without a recognized installer result. Neither summary identifies the later failing assertion. Both retained-retry jobs returned pending for the deliberately unhealthy v0.0.3, with worker manual repair at verify and the manager stopped. These results do not qualify unchanged reruns, upgrades, rollback, recovery, uninstall or coexistence. Diagnostics are being extended using strict check-report fields and bounded harness-only source line numbers, without raw arguments, exception text or journals.

The subsequent source review reproduced two fixture assumptions: an empty manager does not create an `instances` directory until its first instance, and the unchanged-install case reused a preparation output before recording a case. The harness now creates its own private account-owned fixture directory and assigns a fresh output to each preparation attempt. Failure summaries retain strict normalized `install check` reports and up to16 exact harness source line numbers. A service-account executable-access check separates workspace traversal failure from updater staging failure. Sixteen harness and nine scrubber tests pass.

A temporary native composition reproduced rollback failure after removing a newly installed manager unit: generic disable correctly refuses an absent unit. The rollback-specific path now verifies the exact absent/stopped service state. The composition verifies retained inventory and absent manager.db; the general enable/disable API remains strict. Independent review plus integrator install/selfupdate race/vet pass. This still requires native hosted confirmation.

Independent recovery had the analogous snapshot omission for unchanged rollback roles and optional updater trust. A fresh empty-driver-map regression failed before correction; rollback now reinspects and retains exact current snapshots before continuing. The regression verifies all roles and reaches the prior-inventory compare-and-swap guard. Integrator install/selfupdate race, vet and25 Python harness/scrubber checks pass; native interrupted recovery remains unqualified until the hosted run succeeds.

## Upgrade and unchanged-install acceptance reached

Run `39064760030` at `6fba06c0eb336a1c784f8e3cfbf9e06376504ce2` passes absent/authentication refusal, fresh install and unchanged rerun, and v2 upgrade with the independent game fixture on both Ubuntu22.04/24.04. Retained assertions confirm manager PID/SQLite identity preservation for the unchanged rerun and game PID preservation across upgrade. The fixture is still `/usr/bin/sleep`, not Minecraft game evidence.

Both failed-first-install scenarios now report `rolled_back`; the next check returns `drifted`. Both lifecycle scenarios then fail the deliberately unhealthy v3 upgrade rollback. Both coexistence scenarios fail the newly separate `runuser ... test -x` probe against the helper under the GitHub workspace. The helper is now staged at `/usr/local/lib/minecraft-ui/qualification-updater` with verified source/copy bytes and root:root0755 metadata, matching the previously accepted updater fixture layout. Seventeen harness and nine scrubber tests pass. The remaining recovery failures are under source investigation and no scenario is fully accepted.

The retained-state refusal was reproduced: origin validation reconstructed a rolled-back transaction without the required failure stage, although the protected terminal result intentionally does not retain that stage. Immutable intent validation now uses a prepared-phase binding solely for validation after independently proving the protected terminal rollback. The result must have no restored image for a fresh installation; an injected unexpected image is rejected. Production ownership remains UID0.

The upgrade rollback also retained the candidate launch permit after readiness failed. The common rollback path now revokes only the same operation’s permit after stopped-manager/data-lock proof and before replacing assets or retiring the journal. Actual private permit fixtures cover stale candidate revocation and preservation of a syntactically valid foreign operation. The full flow fixture covers failure after candidate launch and fresh rollback revocation.

Integrator review caught a missing test start hook after extending the fixture to readiness failure. One worker test invocation could reach the real service checker. A read-only host check returned not-found/inactive/dead/PID0; the start helper refuses that state before issuing start. The fixture now has an explicit start stub plus a fatal guard against any unexpected systemctl invocation throughout its context. Guarded package race tests pass (install7.539s), updater race, vet and26 Python checks pass. Native acceptance remains pending.

## Retained retry and unhealthy upgrade rollback accepted

Run `39065690483` at `94a62dc69b7d8245d62041e8888d1ffa9b6231fa` passes the complete retained-failure/retry scenario on Ubuntu22.04 and24.04. Both lifecycle jobs also pass fresh installation, unchanged rerun, v2 upgrade and unhealthy v3 rollback with their data-preservation assertions. Neither lifecycle job completes: explicit recovery restores a running v2 manager but the following read-only check reports recovery required. Both coexistence jobs stage their updater fixtures successfully, then fail ordinary manager restart.

Source review and a temporary fixture reproduce two distinct causes. Ordinary startup rejects every nonempty installer operation directory, including valid retained terminal records. Interrupted asset copying creates a named scratch file before recording its ownership; rollback can skip the unchanged public asset and leave that scratch behind. The updater inspector subsequently refuses the unknown scratch. The fixes must preserve refusal of incomplete/foreign records and unknown replacements; removing arbitrary residue is not an acceptable recovery path.

Ubuntu CI `39065670710` at the same commit passes its complete Linux job. The overall CI run fails on Windows, which remains outside the current implementation focus. Full Linux lifecycle qualification remains open.

The staging correction passes independent and integrator source review. Copies use `O_TMPFILE` in the pinned destination directory; exact verified identity is recorded before linking the staging name. A strict optional `anonymous-v1` effect marker distinguishes recoverable pre-link absence from legacy records. Unsupported filesystems fail without a named-copy fallback. Unrecorded scratch is preserved and prevents a terminal rollback claim. Review closed value-comparison and cleanup-alias findings; regressions cover independently decoded effects, immutable markers and changed same-inode scratch. A child-process SIGKILL test verifies kernel cleanup without Go defers, then reconciles the recorded stage through a fresh session. Integrator install/selfupdate race tests and vet pass (install8.161s).

Ordinary startup now uses the bounded terminal-operation scanner, accepting completed records while refusing pending, missing and unknown state. Metadata diagnostics use fixed slots and pinned no-follow directory descriptors, serialize unknown names only as counts, never read file contents, and cap the whole artifact at32KiB. Independent review closed an exceptional descriptor-cleanup path; integrator17 harness and14 diagnostic tests pass. These corrections still require a new native hosted run.

Run `39066971291` at `ab8c421645ec860fdd6d52386d132d9cca8c9968` passes interrupted-worker recovery on both Ubuntu versions, in addition to all earlier lifecycle checks and both retained-retry scenarios. The next assertion exposes a refused-uninstall state leak: the running game and manager remain unchanged, but the game check occurs after creating a transaction, leaving manual repair at quiesce. The game-stopped preflight now runs before host-state snapshots or operation creation; worker rechecks remain. A regression fails before the ordering correction and passes afterward, proving no journal/operation/data creation with private asset paths and a fatal systemctl guard. Independent review and integrator install/selfupdate race (install8.084s) and vet pass. Native uninstall/reinstall remains pending; updater coexistence is still running at this checkpoint. Ubuntu CI `39066971490` passes its Linux job.

## Ordinary updater integration correction

The completed run `39066971291` fails both coexistence scenarios after an ordinary manager restart succeeds. The updater helper exits unsuccessfully; report publication then raises `OSError` at its `os.replace` call, hiding the helper's bounded reports. The hosted errno was not retained, so a cross-filesystem rename failure is an inference. Publication now uses bounded, exclusive copies of the five fixed reports, with no-follow/nonblocking opens, regular single-link checks and final destination identity checks. Sources remain intact, unsafe files are refused, and a foreign replacement is preserved.

Retained metadata shows an imported updater request but no activation journal. Source inspection identifies a missing prerequisite: the legacy fixture created the privileged updater `stages` directory, while fresh native installation did not. A temp-root regression reproduces staging failure without that directory and success after creation. The ordinary updater now creates only that fixed private directory under its existing lock, after request and signed manifest validation, and validates existing ownership, permissions and identity without altering contents. Read-only installer inspection remains unchanged. This source finding still requires hosted confirmation.

Diagnostics now include fixed `stages` metadata and exact ordinary updater unit IDs, plus only allowlisted authored failure markers. Independent source review found no outstanding blockers. Integrator install/selfupdate race tests (8.245s/20.227s), vet, 21 harness tests, 16 diagnostic tests and diff checks pass. All local verification uses private fixtures; no host service or account changes. Full lifecycle and updater coexistence remain open pending the next native run.

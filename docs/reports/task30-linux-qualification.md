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

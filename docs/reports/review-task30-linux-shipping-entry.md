# Review: Task 30 Linux shipping entry and migration

## Scope

Reviewed the current diffs in `cmd/minecraft-ui-bootstrap`, `cmd/minecraft-ui-update`, `release/outer/templates/linux.sh.tmpl` and its producer tests, `deploy/linux/install.sh`, the legacy qualification seeder and three migrated harnesses, `docs/linux-installer.md`, and the related runbook changes. No shipping source files were changed during this review.

## Finding resolved

The reviewed migration initially left [`deploy/linux/QUALIFICATION.md`](../../deploy/linux/QUALIFICATION.md) instructing reviewers to pass a bare binary to the lifecycle shim. The document now points shipping setup to the independently verified release bootstrap `--install` flow and identifies the hosted-only `linux-seed.sh` as fixture setup for the legacy smoke test. A source search confirms the stale command is gone.

## Review notes

- The outer Linux script checks root before downloading when `--install` is selected. The native bootstrap independently checks Linux x64/root before calling `Prepare`, then passes the authenticated receipt's resolved exact `r.Version` to `install apply`.
- The updater dispatch routes `install` commands before ordinary update handling. Its exact `recover` command calls `CheckInstallerStartup` before ordinary updater recovery, allowing a bypass only when the startup check validates a live installer permit. Explicit installer `recover` also invokes the driver's optional `RecoverPending` capability while it owns the protected session, before loading a transaction; apply, check, and startup admission do not reconcile uncertain pending publication. The new ordering regression covers success, refusal, and apply isolation.
- Installer work is launched as a fixed root `systemd-run` transient unit with fixed argv/environment, a validated operation ID and executor path, and bounded lifetimes. Recovery selects `recover-worker`. Caller cancellation can end the `systemd-run` wait while the transient unit remains independently owned by systemd.
- The outer script captures native stdout/stderr privately, limits returned JSON, exposes a bounded terminal result even on failed installation, and removes its private scratch on exit. Bootstrap and updater errors surfaced by these paths are bounded/generic or stage-sanitized.
- The changed game, manager, and updater qualification harnesses now use the explicitly GitHub-hosted, root-only `linux-seed.sh`; repository search found no remaining qualification harness invocation of the shipping lifecycle shim using the old bare-binary syntax.
- [`docs/linux-installer.md`](../linux-installer.md) correctly documents authenticated first install, explicit exact-version selection, local development candidates, check/recover, retained data, and the lifecycle-only shim.
- The follow-up recovery-order wiring review found no additional blocker: the CLI gate and the privileged installer driver's recovery-only reconciliation are both covered by focused tests. The entry remains bounded to fixed command/path contracts; authentication completes before the installer dispatch.

## Verification

- `GOFLAGS=-buildvcs=false go test ./cmd/minecraft-ui-update ./cmd/minecraft-ui-bootstrap ./release/outer ./internal/install ./release/bootstrap` — passed, including recovery-order and generated Linux entry tests.
- `python3 deploy/qualification/linux_installer_test.py` — 10 passed, including explicit updater scenario grammar and workflow wiring fixtures.
- `python3 deploy/qualification/linux_installer_diagnostics_test.py` — 1 passed.
- `bash -n deploy/qualification/linux-installer.sh` — passed.
- Workflow YAML parsing, Python byte-compilation, `bash -n deploy/qualification/linux-seed.sh`, and assigned-file `git diff --check` — passed.
- No installer, account, service, or other host operation was run locally.

## Verdict

The reviewed shipping entry sequence and privilege/recovery wiring look sound from the inspected code and available release-entry tests. The qualification-doc migration finding is closed. No native Ubuntu lifecycle claim follows from this source review.

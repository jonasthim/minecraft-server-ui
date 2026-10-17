# Integrator review: Linux recovery bridges

## Scope and findings

Reviewed `store_recovery_linux.go`, the terminal control preparation bridge in
`selfupdate/installer_control_linux.go`, the retained receipt binding added to
`LinuxUpdaterSnapshot`, and the typed read-only updater inspection result.

The journal recovery bridge keeps ordinary `Load` read-only. Explicit recovery
accepts only a complete, protected transaction with a matching immutable intent,
the expected current journal and a permitted next phase. It synchronizes the
pending file before rename, rechecks both publication operands and the root,
then synchronizes the directory. Review closed missing-file no-op behavior and
the missing pending-file durability fence. Replacement tests now keep the old
inode allocated so their substitution is deterministic.

Terminal control preparation requires the live updater session and its lock.
It validates the captured journal, receipt, request, scratch files, executable
hash and control directory before removing only captured ordinary control
files. Privileged records and executable scratch remain for the later stopped
reconciliation. Review closed missing captured-absence checks for a request or
scratch file that appears after capture. Partial deletion retains the activation
journal so a fresh strict snapshot can classify the remaining state.

The read-only inspection now exposes a terminal flag from the successful strict
snapshot. Nonterminal, ambiguous and error results cannot supply that flag.
Fixtures retain the same filesystem before and after inspection.

## Evidence

Integrator verification passed:

- Combined race tests for install, authenticated inputs, self-update, bootstrap
  and updater commands, qualification producer, bootstrap acquisition and outer
  entry scripts.
- Vet for those packages.
- Focused read-only updater inspection race tests.
- Windows bootstrap/updater cross-build; this is compilation evidence only.
- Actionlint for the Linux installer workflow, 13 Python qualification checks,
  shell syntax checks and `git diff --check`.

One earlier launch-permit fixture failure was reported during concurrent worker
testing. The integrator could not reproduce it in 30 focused repetitions, and
the subsequent combined race run passed. No guard or assertion was removed.

**Verdict: approve these source bridges.** Native installer recovery composition
has a separate review. Actual Ubuntu installer acceptance remains pending the
hosted lifecycle workflow.

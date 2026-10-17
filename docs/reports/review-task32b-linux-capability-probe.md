# Independent review: Task 32B Linux capability probe

## Finding and rereview

### R1 — Resolved: transient units and systemctl query failures are checked

The follow-up adds exact-name `list-units --all` alongside `list-unit-files` and returns failure if either systemctl query fails. Cleanup rechecks loaded units and only stops a unit whose `FragmentPath` is the exact probe unit file path; otherwise it preserves files and reports cleanup failure. Pure fixtures cover existing installed units, loaded units, and failures from each query. The official [systemctl reference](https://www.freedesktop.org/software/systemd/man/latest/systemctl.html) distinguishes loaded units from installed unit files.

The rereview finds R1 resolved. Cleanup's native systemd behavior remains unqualified because no local services or hosted run were invoked.

## Reviewed areas

- The baseline reproduces the updater template's root identity, read/write paths, and sandbox directives. The control adds only `CapabilityBoundingSet=CAP_SETUID CAP_SETGID`; the report correctly describes this as the exact bounding set rather than an additive grant.
- The generated child transition is fixed to the existing `nobody` account and `/usr/bin/id`; output is discarded. Child status is obtained with `waitpid`, not inferred from pipe EOF. The exec failure path now writes a finite errno category while the close-on-exec pipe remains open and returns nonzero; its pure regression fixture checks both the marker and exit status.
- Proc status and UID map reads are bounded, including whitespace-padded UID map parsing and overflow checks. Marker and property summaries reject malformed or duplicate data and emit finite categories. SystemCallFilter output reports literal token presence and mode without treating absent names or groups as proof of syscall availability.
- Helper creation is exclusive before writing and marked owned for cleanup. Temporary state directories are removed only after device/inode/owner identity matches. If stopping the probe services fails, cleanup preserves the unit files and reports failure instead of deleting them under a possibly live service.
- I ran only local syntax and pure fixtures; I did not invoke systemd, create accounts, or touch the host firewall or game.

## Checks

```text
bash -n deploy/qualification/linux-updater-context.sh
bash deploy/qualification/linux-updater-context.sh --pure-fixture
python3 -m unittest deploy.qualification.summarize_unit_context_test
git diff --check -- deploy/qualification/linux-updater-context.sh deploy/qualification/summarize-updater-unit.py deploy/qualification/summarize_unit_context_test.py docs/reports/task32b-linux-capability-probe.md
```

All four checks passed on initial review; the same checks passed again after the R1 correction, including the added pure unit-preflight fixtures. No hosted run has been performed for this packet, so Linux native service behavior remains unqualified.

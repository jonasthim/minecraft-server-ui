# Independent review: Task30L1A authenticated import

## Findings and rereview

The four findings from the initial review are resolved in the correction round.

### R1 — Cleanup reports incomplete removal while preserving replacements

`cleanupInputs` now returns an authored incomplete-cleanup error for absent or identity-changed files and the created `inputs` directory. It removes only paths still matching pinned created identities and preserves foreign replacements. New regressions cover replaced output files and missing/replaced `inputs` roots; they assert the original operation error remains discoverable and the cleanup failure is reported.

### R2 — Prepared and output enumeration are bounded

`readEntries` reads at most 18 entries (17 total names plus an overflow sentinel) and fails closed on overflow. Both prepared source and private destination use this helper. Tests cover the maximum and overflow case.

### R3 — Digest rechecks observe cancellation and enforce byte limits

`hashHandle` now uses a 128 KiB read loop, checks the context between reads, limits consumed bytes to the expected size, and rewinds on return. Its regression first verifies the normal full digest, then cancels on the second context check and confirms at most one buffer was consumed while `errors.Is(err, context.Canceled)` remains true.

### R4 — Acceptance regressions now cover the identified negative cases

Package tests now reject source symlinks and hardlinks, missing and extra prepared entries, non-private production transaction roots, and `OpenMember` path replacement. The private root owner test ran as an unprivileged user in this environment. A new Close test verifies retained creation pins close while committed files remain. Windows native DACL and retained-handle behavior remains a separate runtime gate.

## Reviewed boundary

The source reuses `candidate.VerifyBootstrap` with cloned independently supplied trust, pins an exact version and platform, compares the unsigned receipt against authenticated metadata, and derives output names only from the authenticated member list. Source and destination files are opened relative to retained roots; member copies are hashed against signed size/digest; outputs are created exclusively and pinned; `OpenMember` verifies the actual opened handle and rehashes it before returning. No imported file is executed.

The review is not installer integration or native Windows DACL approval. I made no product source changes.

## Verification

I ran:

```text
go test ./internal/installinputs -count=1
go test -race ./internal/installinputs -count=1
go vet ./internal/installinputs
GOOS=windows GOARCH=amd64 go test -c ./internal/installinputs -o /tmp/installinputs-review.test.exe
git diff --check -- internal/installinputs docs/reports/task30-authenticated-import.md
```

All commands passed on the rereviewed final source, including the retained-pin Close regression. The Windows command is a compile check; no local service, account, firewall, or game access was performed.

## Integrator follow-up

R5 is closed: failures after child-directory creation are tagged; cleanup uses
retained directory identity where available, otherwise reports incomplete
private state and preserves uncertain paths. Independent source rereview found
no further issue; root reran the full scoped race tests after this correction.
Root also restored valid trust after the wrong-key case so the subsequent
repository/version/expiry/existing-output assertions exercise their own guards.
An independent overlay bypassing VerifyBootstrap fails the wrong-key assertion.
Source acceptance is complete for this bounded prerequisite; no installer or
native Windows runtime acceptance is implied.

# Independent review — Task30L5 installer coordinator

## Scope

Reviewed the frozen Task30L5 brief against `internal/install/contracts.go`,
`coordinator.go`, and `coordinator_test.go`. This is source-only coordinator
evidence; it does not qualify a native installer adapter or a shipped
installation lifecycle.

## Findings

### R1 — Resolved: quiescing record stage is in the frozen allowlist

The root updated the frozen brief to include `record_quiescing` in the exact
stage allowlist (Task30L5 brief, line 67). The implementation's stage mapping
therefore matches the current contract.

### R2 — Closed: rollback journal-CAS failure paths have preservation tests

Added deterministic before-publication and after-publication failure cases for
both `rolling_back` and `rolled_back`. The cases verify fixed `manual_repair`
outcomes and stages, preserve both error causes without leaking their text,
confirm the journal's last observable phase, and assert that an unconfirmed
`rolling_back` record prevents rollback while every uncertain terminal record
prevents `Finish`.

## Reviewed behavior

The ordinary phase/effect ordering, initial uncertain-intent stop, detached
five-minute rollback context, error redaction, defensive transaction copies,
committed cleanup behavior, and interrupted-progress recovery are represented
in source and fixtures. `record_prepared` failure does not quiesce or roll back;
the replacing-phase CAS negative control proves `Replace` is not called before
its durable phase. Recovery rolls nonterminal transactions back once and
finalizes committed state without rollback. No native adapter or product
lifecycle claim is inferred from the fixture.

## Verification

- `go test ./internal/install -count=1` — passed.
- `go test -race ./internal/install -count=1` — passed.
- `go vet ./internal/install` — passed.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/install-coordinator-review.test.exe ./internal/install` — passed (compile check only).
- `gofmt -d internal/install/coordinator_test.go` — clean.

## Verdict

**Approve (source-level coordinator only).** The stage allowlist now matches
the frozen brief, and the rollback-record CAS preservation cases pass. This
review does not qualify a native adapter or shipped installation lifecycle.

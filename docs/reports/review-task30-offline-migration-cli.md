# Independent review: Task30 offline migration CLI

## Verdict

The CLI packet passes rereview. The argument grammar, typed summaries, migration dispatch, stopped-state runbook, and corrected error propagation are consistent with the reviewed API and frozen contract.

## R1 — CLI discards safe operation errors — resolved

The earlier version replaced Snapshot/Verify and Migrate/Resume errors with generic strings. The final version returns each library error directly. I inspected the Migrate/Resume return paths: they consistently wrap failures with `maintenanceFailure`, whose displayed message is a fixed authored stage and whose unwrap value is limited to `context.Canceled` or `context.DeadlineExceeded`. The CLI therefore preserves useful bounded diagnostics and `errors.Is` cancellation behavior without exposing paths, SQLite messages, credentials, or OS detail.

`TestManagerStateMigrationErrorsRemainFixedAndPreserveCancellation` checks a fixed safe failure message, empty output on failure, and `errors.Is(err, context.Canceled)`. Snapshot and Verify also retain their prior direct error propagation.

## Runbook and acceptance coverage

The fixed command forms and flag order match the implementation. `parseManagerSchema` accepts only positive canonical base-10 `int64` strings, and malformed/reordered/extra arguments are rejected before configuration is read. The dispatched integration test performs a real temporary schema-1 fixture migration, verifies the startup hold, confirms startup refusal before resume, then explicitly resumes. The runbook accurately requires a stopped manager, documents Windows elevation and fixed C: NTFS constraints, keeps startup held until explicit resume, and directs operators not to delete `hold.json`/`hold.pending` manually or resume a `migrating` hold. It does not instruct automatic database/world restoration.

## Verification and limits

The following checks passed on the final rereviewed source state after R1 was corrected:

```text
go test -count=1 ./cmd/minecraft-ui
go test -race -count=1 ./cmd/minecraft-ui
go vet ./cmd/minecraft-ui
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/minecraft-ui-task30-cli-rereview.test.exe ./cmd/minecraft-ui
git diff --check -- cmd/minecraft-ui/manager_state.go cmd/minecraft-ui/manager_state_test.go docs/RUNBOOK.md docs/reports/task30-offline-migration-cli.md
```

The Windows dispatched fixture uses production authorization and the fixed local NTFS fence; it was not executed here. A Windows cross-compile does not qualify that behavior. No native Windows administrator run is claimed.

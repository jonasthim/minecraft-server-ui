# Task30 offline migration CLI

## Implemented boundary

The `manager-state` command now accepts these exact forms:

```text
manager-state snapshot
manager-state verify --snapshot HEX_ID
manager-state migrate --snapshot HEX_ID --from-schema N --to-schema M
manager-state resume --snapshot HEX_ID --confirm-manager-state-review
```

Migration flags must appear in the shown order. Schema values must be canonical
positive base-10 `int64` values: signs, whitespace, leading zeroes, zero,
negative numbers and overflow are rejected. All forms reject duplicate,
unknown, reordered and extra arguments before reading configuration or
performing filesystem work.

The CLI passes the configured data directory and build version to the reviewed
`managerstate.Migrate` and `managerstate.Resume` APIs. Success emits one typed
JSON summary. Migrate and Resume preserve the library's finite authored error
categories and cancellation/deadline identity; raw path, SQLite, credential or
operating-system details are not exposed. Existing Snapshot and Verify error
behavior is unchanged. The CLI does not start services or game servers.

The stopped-manager section in `docs/RUNBOOK.md` documents the operator sequence
and platform requirements. A successful migration leaves startup held until an
explicit resume. Failed/interrupted migration stays held for recovery; the
runbook directs operators not to remove hold files manually or use resume on a
`migrating` hold. Resume does not rewind manager worlds and does not start the
manager. Restore/recovery remains a separate future operation.

## Verification

Tests cover canonical schema parsing, the exact argument grammar, safe generic
error output, and an actual dispatched migration from a real schema-1 SQLite
fixture plus a verified raw Snapshot. On Linux the dispatched integration test
uses the production running-image hash and Linux directory/file durability
fence, verifies the JSON summary and startup hold, confirms `serve` refuses to
start before resume, then dispatches explicit resume and confirms the hold is
removed. The fixture uses temporary data only.

On Windows the same dispatched test is intentionally not skipped: it exercises
the production authorization and fixed local C: NTFS fence and therefore must
run with LocalSystem or an elevated Administrators token. If the native test
job lacks that authority, the test fails and the Windows CLI qualification is
unavailable; a cross-compile is not a substitute.

Commands passed on Linux:

```text
go test -count=1 ./cmd/minecraft-ui
go test -count=1 -race ./cmd/minecraft-ui
go vet ./cmd/minecraft-ui
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/minecraft-ui-cli.test.exe ./cmd/minecraft-ui
git diff --check
```

Full package, race, vet and Windows cross-compile checks are recorded after
integration review. No native Windows administrator run is claimed here.

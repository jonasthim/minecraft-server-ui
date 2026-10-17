# Independent review: Task30L4B offline migration

## Verdict

The reviewed offline migration package addresses the two findings in this review. I found no remaining blocker in the SQLite path binding or connection-scoped durability settings. This review does not approve package completion or native qualification.

## R1 — SQLite path substitution during connection setup — resolved

The initial version opened SQLite through the absolute `dataDir/manager.db` path while checking only the retained `os.Root` identity. A replacement of the absolute data directory during connection setup could therefore point SQLite at a different file.

The final implementation verifies the absolute data-root binding before `sql.OpenDB`, before and after each physical SQLite connection, after initial `PingContext`, and after `Close`; it also checks database identity through the retained root. The DSN is restricted to `mode=rw`, with no mutating `_pragma` options. The connector configures WAL, foreign-key enforcement, FULL synchronous mode, and busy timeout only after validating the physical connection's root and database identity, then verifies those settings and rechecks identity before returning the connection.

`TestMigrateRejectsDataDirectorySubstitutionAtSQLiteConnectionBoundary` injects replacement before Ping, after the driver opens the connection but before connection settings, and after Ping. Each case asserts migration refuses the substitution, the replacement database bytes remain unchanged, and the original data root retains its startup hold.

## R2 — connection pool replacement could lose durability pragmas — resolved

`database/sql` may create another physical connection if a prior one is discarded. Applying pragmas once through `*sql.DB` does not guarantee connection-local settings on that replacement. `migrationConnector.Connect` now wraps the modernc SQLite connector and applies/verifies all required settings on every physical connection before it is returned to `database/sql`.

`TestMigrationConnectorReappliesRequiredPragmasOnReplacementConnection` opens two separate physical connections through the wrapper, closing the first before opening the second, and asserts `journal_mode=wal`, `foreign_keys=1`, `synchronous=2`, and `busy_timeout=5000` on both. This directly exercises the connector path used for pool replacements.

## Startup wiring follow-up

The separate `serve` change acquires the manager lock, calls read-only `managerstate.CheckStartup`, and returns on a maintenance hold before constructing the supervisor or opening SQLite. `TestServeMaintenanceHoldPrecedesDatabaseOpen` covers both `hold.json` and `hold.pending` with existing and missing database cases; it asserts existing database bytes and hold bytes remain unchanged and that startup does not create a missing database. `go test -count=1 ./cmd/minecraft-ui` passed on Linux. This is focused package evidence, not native Windows qualification.

## Verification

The following checks passed on the final reviewed source state:

```text
go test -count=1 ./internal/managerstate
go test -race -count=1 ./internal/managerstate
go vet ./internal/managerstate
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/managerstate-task30l4b-reconnect-review.test.exe ./internal/managerstate
git diff --check -- internal/managerstate docs/reports/review-task30-offline-migration.md
```

Windows is compile evidence only. I did not run native services, migration, or filesystem operations on Windows or an installed Linux account. No native qualification or ledger update is included in this review.

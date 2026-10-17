# Task30L4B offline migration and startup hold

## Implemented boundary

This packet adds the frozen `managerstate.CheckStartup`, `Migrate`, and
`Resume` API. It does not add CLI or startup wiring; root owns those changes.
Migration acquires the existing manager lock, retains checked data and private
maintenance roots, verifies the supplied raw snapshot and its independent
working copy, and compares the complete current SQLite file set and bytes with
the snapshot before publishing a hold. The hold records the source/target
schema, snapshot manifest digest, requested binary version, and SHA-256 of the
running executable. Live SQLite opens only an existing database in `mode=rw`,
requires WAL, foreign keys, `synchronous=FULL`, and a bounded busy timeout, and
validates the live source schema before applying embedded migrations. It then
checks integrity, foreign keys, core schema, and Goose ledger before closing
SQLite and changing the exact held state from `migrating` to `migrated`.

The absolute data-directory binding is checked immediately before the first
SQLite connection, after its first successful ping, and again after every
database close in Migrate and Resume. The rooted `manager.db` identity checks
remain in place on both sides of the connection lifetime. Deterministic tests
swap the absolute data root before Ping, after the driver opens the connection,
and after Ping; migration refuses each substitution, leaves the hold in the
original root, and the replacement database remains byte-for-byte unchanged. The
connection DSN has no mutating pragma options: WAL and the remaining required
connection settings are applied only after the post-ping root and file identity
checks. A checked `driver.Connector` wraps every physical SQLite connection.
Each connection checks the retained root and database identity before and
after the underlying open, applies WAL, foreign keys, FULL synchronous mode,
and the bounded busy timeout only after those checks, verifies all four live
values, then rechecks the bindings before returning the connection to
`database/sql`. Pool replacement therefore gets the same validation and setup.

The exclusive `hold.pending` file remains pinned through write, sync, rooted
rename, identity/content checks, and the platform fence. Failures after a hold
may have been published leave startup blocked and preserve the raw recovery
snapshot. Resume requires explicit confirmation, the same snapshot/version/
executable identity, a `migrated` hold, a valid current database and a second
snapshot verification. It removes only the exact held state and reports fence
or removal uncertainty without a success result. `CheckStartup` is read-only;
it allows a missing safe data root or absent maintenance directory, and blocks
on any `hold.json` or `hold.pending`, including malformed and unknown contents.

The shared `validateSQLiteHandle` extraction lets migration validate the
already-open database under the retained lock without opening another
connection or reacquiring the lock. Existing Snapshot and Verify behavior is
unchanged.

## Durability and authorization

Linux syncs the retained data and maintenance directories after the hold
publication, phase transition, and removal. Windows migration and resume
require LocalSystem or an elevated Administrators token. The Windows fence
rechecks the retained data and maintenance directory identities, then requires
the data path to reside on the fixed local C: NTFS volume and flushes that
volume. Custom volumes, mount paths, and non-NTFS storage fail closed.

The existing Snapshot Windows directory-sync helper is a no-op. This packet
does not claim that helper qualifies offline migration durability; migration
uses the separate C: NTFS volume fence. Native Windows administrator/runtime
qualification remains required. Linux package tests used temporary owned
directories and do not qualify the installed manager account or deployed
filesystem.

## Acceptance evidence

Tests use an actual older embedded schema, Goose ledger, a real matching
Snapshot, and SQLite migration code. They verify target schema and preserved
records, unchanged raw snapshot hashes, startup hold until explicit Resume,
missing database non-creation, and no hold publication on stale database
bytes, sidecar mismatch, missing snapshot, source/target schema mismatch,
wrong target, or concurrent manager lock. Additional tests cover preexisting
foreign `hold.json` and `hold.pending` preservation; post-publication failure
and cancellation leaving startup blocked; path replacement preservation;
Resume refusal without confirmation or with a changed executable hash, wrong
snapshot ID, `migrating` phase, or changed referenced snapshot.
The connector regression opens two separate physical connections and checks
WAL, foreign keys, synchronous mode, and busy timeout on both. Directory
substitution is injected before Ping, after the driver opens the connection,
and after Ping; each case refuses the operation, preserves the original-root
hold, and leaves the replacement database byte-for-byte unchanged.
The fixture uses Goose's current SQLite ledger column (`tstamp`).

Two isolated Go source overlays checked that core regressions detect bypasses.
Replacing `CheckStartup` with a no-op made
`TestCheckStartupBlocksForAnyHoldOrPendingState` fail for both hold names.
Replacing snapshot comparison and source-set verification with no-ops made
`TestMigrateRejectsInvalidOrStaleInputsBeforePublishingHold` fail for stale
database bytes and a sidecar mismatch.

Commands passed:

```text
go test -count=1 ./internal/managerstate
go test -count=1 -race ./internal/managerstate
go vet ./internal/managerstate
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/managerstate-maintenance.test.exe ./internal/managerstate
git diff --check
```

The Windows command is a compile check only. No native Windows administrator
run, installed Linux account run, CLI/startup integration, lifecycle restore,
or implementation-ledger completion is claimed by this report.

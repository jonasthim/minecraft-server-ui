# Independent review: Task 30L4A manager-state snapshots

## Result

The package has meaningful coverage for WAL retention, source preservation,
strict manifests, historical schema acceptance, locking, and unknown-file
refusal. Focused race tests, vet, and Windows compilation pass. I found two
material gaps against the frozen cleanup and cancellation requirements; this
packet should be corrected before integration.

### 1. Cleanup identity is not retained for created files

`copyNamed` records `out.Stat()` as `os.FileInfo`, closes the output, and keeps
only that metadata in `snapshotFiles` or `validationFiles`. `writeSyncedExclusive`
does the same for the manifest. `removeOwnedTree` later trusts `os.SameFile`
between that stale metadata and the current pathname before deleting the file.
No descriptor or file handle pins the created identity until cleanup.

After an owned file is unlinked, its inode or Windows file ID can be reused.
If a replacement at the same name receives that identity, cleanup can treat the
replacement as owned and delete it. The test suite covers unknown files and
replaced directories, but has no unlink-and-recreate replacement-file case.
An isolated Linux churn check did not reproduce inode reuse on this filesystem
after 20,000 immediate recreations, so the risk is not reported as an observed
local failure. The implementation still relies on a reusable identifier for a
security property that the retained-handle correction in Task30L0 addressed
for the same reason. Retain the actual creation handles for snapshot and
validation files through cleanup, or use an equivalent identity mechanism that
prevents reuse, and add a replacement cleanup test.

### 2. A mid-copy cancellation can strand a partial snapshot and lose its cause

In `copyCaptured`, cancellation at the start of a later file iteration returns
`nil, nil, err`, discarding the identities accumulated for earlier files. The
caller assigns that nil map to `snapshotFiles`; failure cleanup then sees files
in the snapshot directory that are absent from the ownership map and correctly
refuses to remove them. That leaves a partial private snapshot and reports
cleanup incomplete. On errors from `copyNamed`, the function instead returns a
generic authored error, which loses `errors.Is(err, context.Canceled)` when the
underlying cause was cancellation. Similar fixed wrappers in `verifySnapshot`
and `copySnapshotForValidation` also discard cancellation identity.

The existing cancellation test cancels in the `afterCopy` seam, after all
copies have completed, so it does not exercise either failure. Preserve the
partial ownership map on every `copyCaptured` return and wrap underlying
errors so cancellation identity survives; add a deterministic cancellation
case with multiple source files that cancels between copies and checks cleanup
and `errors.Is`.

## Reviewed behavior

- Both entry points validate the existing data directory and ordinary
  `manager.db` before acquiring `launcher.AcquireManager`, then recheck the
  data-directory binding and source set under that same existing manager lock.
  The lock remains held through snapshot/validation cleanup and closure of the
  maintenance and snapshot roots. The outer data root is closed after lock
  release. The package does not open the source database through SQLite or
  invoke a migration opener.
- SQLite validation uses a separate copied database with the normal WAL
  recovery behavior, checks integrity and foreign keys, validates supported
  schema and Goose ledger state, and does not migrate. The subprocess WAL test
  demonstrates the committed marker is present only when the retained WAL is
  copied. Raw source bytes are checked before and after snapshot/verify.
- Manifest decoding rejects duplicate and unknown fields. The snapshot
  directory must contain exactly the sorted declared members plus
  `snapshot.json`; member sizes and hashes are checked before a fresh
  validation copy is opened. Schema 1 is accepted without migration.
- Unix creation uses 0700 directories and 0600 files, with owner, link-count,
  and file-type checks. Windows creates directories with a protected DACL for
  the manager SID, Administrators, and System and checks it before reuse. These
  Windows ACL checks were compiled but not executed natively in this review.
- The SQLite working copy is opened by an absolute path derived from the
  retained maintenance path, while file creation/copy/cleanup use `os.Root`.
  The maintenance tree is private to the manager account boundary and bound by
  identity checks. I did not demonstrate a path replacement through that
  boundary; native Windows qualification remains necessary.

## Verification

- `go test -race ./internal/managerstate ./cmd/minecraft-ui -count=1` — passed.
- `go vet ./internal/managerstate ./cmd/minecraft-ui` — passed.
- Windows amd64 test binaries for `internal/managerstate` and
  `cmd/minecraft-ui` compiled successfully; they were not executed.
- A bounded Linux filesystem check repeatedly unlinked and recreated one file
  20,000 times and did not observe inode reuse. This does not establish that
  reuse cannot occur on other filesystems or under different allocation
  patterns.

No source files were edited as part of this review. No local preview, native
service, account, firewall, installer, or game was operated.

## Addendum: Windows owner checks and bootstrap cleanup order

### 3. Windows owner validation compares SDDL text by prefix

`checkPrivateDirectory` checks `sd.String()` with `strings.HasPrefix` for the
manager SID, Built-in Administrators, and Local System. Microsoft documents
that SDDL SID strings in the owner component may use either numeric SID strings
or well-known SID aliases such as `BA` and `SY`; the `SECURITY_DESCRIPTOR`
string conversion can therefore return a valid owner as an alias that these
numeric prefixes reject. The manager check also accepts a different longer
numeric SID whose text begins with the manager SID. Read the owner SID from the
descriptor and compare the parsed manager SID for exact equality, while
checking Administrators and System with `SID.IsWellKnown` (or equivalent
canonical SID equality). See [Microsoft SID Strings](https://learn.microsoft.com/en-us/windows/win32/secauthz/sid-strings) and [Security Descriptor String Format](https://learn.microsoft.com/en-us/windows/win32/secauthz/security-descriptor-string-format).

The current manager-state fixtures create their data directories with
`t.TempDir()` and let the host temporary-directory DACL flow through. On
Windows, `platformManagerSID` requires exactly one writable domain-account
principal in the data-directory DACL. That fixture setup does not make the
required principal set explicit and can vary with the runner's inherited ACL.
The Windows fixture should create a temporary data directory with a known
manager-account DACL (manager SID, Administrators, System), then initialize the
database within it. This is a fixture-only recommendation; no native Windows
execution was performed during this review.


## Integrator closure

R1–R3 are closed after source inspection: created snapshot and validation files
retain their actual handles through owned cleanup; partial ownership survives
every cancellation return and fixed errors preserve cancellation identity;
Windows owner validation uses parsed SID equality and well-known SID checks.
Root's additional R4 requires SQLite `mode=rw` and directory/database binding
checks around connection and close; these checks are implemented. Explicit
Windows data DACL fixtures and correctly named platform test files are included.

The validation-path race fixture closes its test file pin to permit a directory
substitution on Windows, retaining the directory binding checks. It then asserts
that SQLite creates no database in the substitute directory. An independent
root overlay removing `mode=rw` fails exactly that assertion, proving this
fixture detects the unwanted creation. Separate tests cover retained file pins
and replacement-preserving cleanup.

Root ran focused manager-state/CLI race tests (including dispatched snapshot
and verify), Windows test compilation and Windows vet successfully. The earlier
full scoped race/vet checks also passed. Source and CLI integration are accepted;
native Windows ACL/WAL execution, installed-service use, migration/restore and
physical power-loss qualification remain separate gates.

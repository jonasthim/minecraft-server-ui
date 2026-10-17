# Independent review: Task30L1B maintenance receipt bridge

## Result

No blocking privilege-boundary or stale-receipt bypass found in the reviewed bridge. This review is source-level; Windows ACL, retained-handle, replacement-rename, and durability behavior remain subject to native Windows evidence.

## Reviewed behavior

- `AcquireMaintenance` checks context, then platform authority before opening roots or the lock. Linux requires effective UID 0; Windows accepts LocalSystem or an elevated token that is a member of Administrators. Production hooks use fixed platform paths and cannot be configured through the exported API.
- Both state and binary roots use the existing platform protected-root checks. Root bindings are re-opened and compared before and after taking `launcher.AcquireManager(paths.State)`. The manager lock file is left in place and its OS lock remains held until `Close`.
- Conflict checks use the source's actual native updater filenames: `activation.json`, `activation.pending`, `request.json`, `launch.json`, `launch.pending`, and `installed.pending`, plus the bridge pending name. The Linux launch permit is indeed `launch.json` / `launch.pending`, not the brief's initial `launch-permit.json` label. The separate `data/selfupdate/control/request.json` is inspected when its safe ancestry exists; unsafe open errors fail closed. Existing `activation.json` is refused, with no interpretation or cleanup.
- Receipt capture pins the opened `installed.json` handle and stores the raw bytes, digest, and metadata. CAS checks session ownership, current path and handle identity, metadata, bytes and digest; absent captures require that the receipt remain absent. The candidate hash is streamed from the protected canonical executable path and rechecked immediately before publication.
- Publication uses exclusive creation of `installation-receipt.pending`, checks the opened handle and path binding, and preserves a replacement during cleanup. The prior and executable are rechecked before rooted rename. A post-rename fence failure reports `publication uncertain`; the code does not claim rollback or success.
- `Close` closes receipt/publication pins and roots before releasing the updater lock. Closed sessions reject `Receipt` and CAS calls. The tests cover cross-process lock exclusion, cancellation cleanup, same-size byte changes, path replacement, foreign/closed sessions, pending replacement preservation, executable hash mismatch, short writes, and post-rename fence failure.

## Test coverage gap

The source has the required conflict-file list, but `maintenance_receipt_test.go` does not directly assert acquisition/CAS refusal and preservation for each listed conflict, especially the actual `launch.json` / `launch.pending` names and `data/selfupdate/control/request.json`. Add a table-driven fixture covering each path, including an unsafe control ancestry case; this would protect the deliberately conservative reconciliation boundary against future filename drift.

`MaintenanceReceipt.Value()` continues returning its immutable captured value after its owner closes. I interpret the brief's “methods after Close” requirement as applying to session operations (`Receipt` and CAS), since `Value` is a detached snapshot with no error return. If the contract instead intends snapshot access to end at `Close`, its boolean should report false after closure.

## Verification

- `go test -race -count=1 ./internal/selfupdate` passed.
- `go vet ./internal/selfupdate` passed.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/selfupdate -o /tmp/selfupdate-maintenance-review.test.exe` passed.
- These checks do not establish native Windows ACL or rename behavior.

## Integrator follow-up

The conflict matrix now directly covers all seven state names and the control
request at acquisition and after acquisition before CAS, including byte
preservation and lock release on refusal. Unsafe control ancestry is refused;
safe absence is accepted. Root reran the selfupdate race suite and vet after
this addition. Snapshot Value remains an immutable captured value after Close;
it provides no operation authority, and every session mutation still rejects a
closed or foreign session. This interpretation is accepted.

Root checked the installed Go runtime's Windows rooted OpenFile implementation:
it uses share-delete handles, unlike ordinary syscall.Open. The bridge's pinned
receipt and pending handles use the rooted API, so their presence is not itself
a reason to close pins before rename. Native execution must still demonstrate
replacement and retained-handle behavior. Fresh install and retained-state
reinstall need a separate acquisition capability: this entry currently requires
an existing protected manager executable and must not be silently widened.

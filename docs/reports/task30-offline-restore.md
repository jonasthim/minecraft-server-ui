# Task30L4C — offline manager-state restore

## Scope and safety boundary

Restore replaces the manager SQLite state from a selected, fully verified
snapshot. It leaves Minecraft worlds, runtime files, and services untouched.
The manager lock stays held while the target is staged, the old database set is
preserved, SQLite validation runs on a separate working copy, and durability
fences complete. A format-2 restore hold blocks startup until the operator
reviews the result and calls the existing explicit Resume path with the
expected image and schema. A failed or canceled operation keeps the hold and
does not roll back or start anything automatically.

Restore first creates and verifies a fresh safety snapshot of the current
manager state. The immutable operation intent records snapshot hashes, schemas,
executor and resume bindings, and native file identities for every current and
staged database/sidecar file. All current files move to the private preimage
directory before any target file moves into the data directory. Re-entry with
the same explicit confirmation resumes only when every recorded identity and
file location is unambiguous. The prior format-1 migration hold is preserved as
an exact operation preimage.

On Linux, each rename fence flushes the destination directory first, then the
source directory, then the operation ancestor before the existing data and
maintenance directory fence. This preserves the destination directory entry
before persisting the source removal for both data-to-preimage and
stage-to-data moves. On Windows, the existing fixed local C: NTFS administrator
volume fence covers the move; directory handles are not treated as durable.
Validation rechecks all live target hashes and native identities after the
separate SQLite working-copy checks and before the hold can advance or clear.

## Interfaces and files

The public package API is `managerstate.Restore(ctx, dataDir, binaryVersion,
RestoreOptions)`. The options require `Confirmed` and bind a snapshot. An older
target schema also requires an explicit future resume version and executable
SHA-256. The returned summary adds only safety snapshot ID, operation ID,
schema values, and the expected executable hash. Restore does not install or
assert provenance for that future image.

The package implementation is in `internal/managerstate/restore*.go`, with a
locked snapshot helper extracted in `managerstate.go`. Existing migration hold
parsing and Snapshot/Verify/Migrate behavior remain intact. CLI and runbook
integration are tracked separately by the integrator.

## Verification

The package tests use temporary, real SQLite databases and raw snapshots. They
cover restored manager records, safety snapshot recovery, committed WAL
contents, untouched world marker, explicit hold/resume, lock retention,
interruption after old-file and target-file moves, same-bytes replacement at a
different native identity, unsafe hardlinked sidecars, unexpected live SQLite
sidecars, invalid current database refusal, prior migration-hold preservation,
wrong resume bindings, and context cancellation. A deterministic post-validation
hook replaces the live database; the final raw identity/hash check rejects it,
preserves the foreign bytes, and leaves startup held. A directory-sync recorder
observes destination-before-source ordering on the actual production rename
path.

Checks run:

- `go test ./internal/managerstate -count=1` — passed.
- `go test ./cmd/minecraft-ui -count=1` — passed with the root-owned CLI integration present in the shared tree.
- `go test -race ./internal/managerstate -count=1` — passed.
- `go vet ./internal/managerstate` — passed.
- `GOOS=windows GOARCH=amd64 go test ./internal/managerstate -run '^$' -exec=true` — compiled successfully; this is cross-compilation only.
- Isolated Go overlay bypassing the native identity equality made `TestRestoreContinuationRejectsSameBytesAtReplacementIdentity` fail because continuation accepted the replacement.
- Isolated Go overlay bypassing complete live SQLite file-set verification made `TestRestoreResumeRefusesExtraSQLiteSidecarAndPreservesIt` fail because Resume accepted the unmanifested sidecar.

Native Linux durability and native elevated Windows C: NTFS execution remain
qualification gates. No local service/account/game actions or native mutation
were performed for this packet. The integration owner must review the evidence
and update the task ledger before claiming acceptance.

# Task 30L4C offline restore source review

## Scope and outcome

Reviewed the frozen Task 30L4C brief and the offline restore implementation in
`internal/managerstate/restore*.go`, plus its maintenance and manager-state
helpers. The review found three material issues during implementation; the
owner corrected them before final review. **Source review passes with no open
findings.** Native administrator filesystem qualification remains pending.

## Corrections verified

1. **Rename durability ordering.** A restore rename now synchronizes the
   destination directory before the source directory, then runs the existing
   data/maintenance durability fence. This preserves the correct direction for
   both `data → preimage` and `stage → data` moves. Preparation also synchronizes
   its nested directories before publishing the hold. A focused regression
   asserts destination/source order on the actual move path. This matters on
   Linux because syncing file data does not itself guarantee directory-entry
   persistence; see [Linux `fsync(2)`](https://man7.org/linux/man-pages/man2/fsync.2.html).

2. **Cancellation during hashing.** Restore file verification now checks the
   context while hashing, and callers pass the operation context through
   location and layout checks. The restore does not need to finish repeated
   full-file hashes after cancellation.

3. **Live bytes after working-copy validation.** After validating and cleaning
   the separate SQLite working copy, restore now rechecks every target file's
   identity and digest and the exact live SQLite file set before returning to
   the caller. A regression mutates the live database after SQLite validation
   and confirms restore fails, preserves the replacement, and leaves startup
   held.

4. **Bounded recovery-directory reads.** Operation, stage, and preimage
   directory enumeration now reads at most the expected entry count plus one,
   then rejects overflow. This prevents an untrusted directory from causing an
   unbounded `ReadDir(-1)` allocation before exact-set rejection.

The reviewed flow also binds executor/resume identities and both snapshots in
the strict intent and hold, validates old and staged files by native identity
plus content, moves the entire old file set before target placement, retains
preimages and safety snapshots, and requires explicit matching-image Resume.
Windows uses its fixed-volume durability fence; the Unix per-directory sync
ordering test is intentionally skipped there.

## Verification

- `go test ./internal/managerstate -count=1` — passed.
- `go test -race ./internal/managerstate -count=1` — passed.
- `go vet ./internal/managerstate` — passed.
- `GOOS=windows GOARCH=amd64 go test -c ./internal/managerstate` — passed.

These checks establish package-level source behavior and Windows compilation;
they do not establish native Windows filesystem or administrator qualification.

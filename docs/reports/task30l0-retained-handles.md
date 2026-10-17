# Task 30L0: pin bootstrap-created file identities

## Observed failure and cause status

Candidate run `39042940720` failed `TestPreparePreservesSubstitutedAuthenticatedInput` on Linux: after the acquisition callback replaced the authenticated metadata pathname, cleanup removed the replacement. The likely explanation is inode reuse. Before this change, `ownedFile` retained only `os.FileInfo`; the caller closed the only descriptor for the created file before replacement and cleanup. If unlink and recreation reused that inode, `os.SameFile` could not distinguish the replacement from the created object. This is a source-backed cause hypothesis, not a confirmed reproduction of that hosted failure. Native Windows qualification remains pending.

## Correction

Each destination file opened with `O_EXCL` now gets a duplicate of its actual creation descriptor immediately. Unix uses `syscall.Dup` and marks the duplicate close-on-exec. Windows uses `DuplicateHandle` with `DUPLICATE_SAME_ACCESS`; this preserves the original handle's sharing permissions. The code does not reopen the pathname to establish ownership.

The retained handle remains authoritative after the caller closes its writer. Ownership checks stat the pinned handle and compare it with the current root-relative path, while preserving regular-file, no-reparse, and single-link checks. Cleanup removes matching identities while all pins remain open, then closes the pins before removing the destination directory. This ordering avoids a Windows delete-pending file preventing directory removal; the destination root remains held for its binding check. Successful `destination.close` closes pins without removing successful output. Duplicate failure retains the original creation descriptor as cleanup authority and fails the operation.

## Regression coverage

- `TestDestinationPinPreservesCreatedIdentityAgainstReplacement` proves the pinned descriptor differs from and outlives the caller writer; after unlink/recreate with identical bytes, the replacement has a distinct identity, owned removal refuses it, and cleanup preserves its identity and contents.
- `TestDestinationPinsCloseAfterSuccessAndCancellationCleanup` verifies successful close releases pins while preserving committed output, and failure cleanup after cancellation releases pins and removes the incomplete destination.
- Existing `TestPreparePreservesSubstitutedAuthenticatedInput` remains intact and now passes repeatedly. Existing snapshot-replacement and Windows parent-rename tests remain enabled; the Windows parent test checks the same rename after `Prepare` returns, detecting leaked handles or weakened sharing assumptions.

## Verification

- `go test -race ./release/bootstrap -count=1` — passed.
- `go vet ./release/bootstrap` — passed.
- `GOOS=windows GOARCH=amd64 go test -c -o /tmp/bootstrap-retained-handles.test.exe ./release/bootstrap` — passed; this compiles the Windows tests but does not execute them.
- The substitution, pin lifecycle, and existing snapshot-replacement tests passed with `-count=30` on Linux.
- `git diff --check` on the owned source and test files — passed.

The candidate failure's inode-reuse explanation remains an inference until reproduced or addressed by a new native run. No native Windows operation, service, account, installer, firewall, game, or preview process was used. No commit was made; integration and commit remain with the root agent.

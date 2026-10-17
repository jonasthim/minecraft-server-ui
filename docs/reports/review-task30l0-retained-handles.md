# Independent review: Task 30L0 retained creation handles

## Result

No blocking source issue found in the retained-handle correction.

`destination.create` duplicates the descriptor returned by the exclusive
creation call itself. It does not reopen a pathname to establish identity. The
duplicate is validated against the original creation descriptor and the
root-relative pathname before being stored. Subsequent ownership checks stat
the pinned descriptor, retain the regular-file/single-link/no-reparse guards,
and compare the current path binding to that pinned identity. This keeps the
created inode/file ID alive across writer close and pathname replacement, so
the same identity cannot be reclaimed through inode or file-ID reuse while the
operation still depends on it.

The pins remain in `destination.files` until cleanup or destination close.
Failure cleanup checks and removes matching owned files while pins remain
open, then closes the pins. A mismatched replacement remains untouched and
causes cleanup to report incomplete state. On success, the deferred
destination close closes the pins without removing prepared output. Cleanup
also records close failures as incomplete cleanup. The success close path
attempts every pin close; its existing void close boundary does not return a
close error.

The platform implementations match the intended sharing model: Unix uses
`dup` and marks the descriptor close-on-exec; Windows uses
`DuplicateHandle(DUPLICATE_SAME_ACCESS)` with a non-inheritable handle. That
preserves the original handle's access and sharing modes. The existing parent
rename test is honest about Windows behavior: it accepts either a successful
rename while handles are open or a permission denial; when denied, it verifies
the same rename succeeds after `Prepare` returns and the handles are released.
It does not require Windows to deny a rename.

The change retains the existing exclusive creation, exact Unix mode, regular
file, single-link, no-reparse, directory ancestry, destination binding,
authenticated input byte checks, and cleanup guards. The new replacement test
checks that the pin outlives the writer, that an identical-content replacement
does not share the pinned identity, and that cleanup preserves the replacement.
The existing `TestPreparePreservesSubstitutedAuthenticatedInput` covers the
original end-to-end replacement behavior. The lifecycle test checks pin close
on successful destination close and on destination cleanup; its canceled
context is only a fixture marker, since cleanup itself has no context input.

## Verification

- `go test -race ./release/bootstrap -count=1` — passed.
- Focused substitution, pin lifecycle, and snapshot replacement tests with
  `-race -count=30` — passed.
- `go vet ./release/bootstrap` — passed.
- `GOOS=windows GOARCH=amd64 go test -c -o /tmp/task30l0-review.test.exe ./release/bootstrap` — passed; Windows tests were compiled, not executed here.

The earlier candidate run `39042940720` recorded the baseline
`TestPreparePreservesSubstitutedAuthenticatedInput` failure on Linux. The
reported inode-reuse explanation remains a plausible source-backed hypothesis,
not a confirmed reproduction of that exact hosted failure. This review did
not run a separate local old-source negative control or native Windows tests.
No local service, account, installer, firewall, game, or preview process was
operated.

## Cleanup ordering addendum

I reviewed the cleanup-order adjustment in `release/bootstrap/files.go`.
`destination.cleanup` attempts every owned-file removal while identity pins
remain open, closes all pins, then checks and removes the destination directory
through its retained parent root. This releases Windows file handles before
parent directory removal, which accommodates delete-pending semantics. Pin
close failures mark cleanup incomplete. The existing pin lifecycle test
asserts that the canceled destination is absent after cleanup and can verify
this on native Windows. Focused Linux race tests passed 10 repetitions and the
Windows test binary cross-compiled; native Windows behavior remains
unverified here.

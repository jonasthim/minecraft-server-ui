# Task30A1 acquisition fix round 1 independent review

Reviewed frozen candidate `f6d29066053cd4cac9ec16312d2b6997dcd31544`
against base `9ee62da`, limited to the four files in the frozen fix diff.
Continued the original review against the acquisition brief, approved design,
plans, ledger and candidate contract. Other concurrent packets are excluded.

**Specification: PASS. Quality: APPROVE. R1 closed; zero open or new findings.**
This accepts the scoped acquisition source correction only.

## R1 closure

`release/bootstrap/prepare.go:204` now opens the snapshot through
`destination.openOwnedRegular`. That helper checks the creation identity before
opening, compares the opened handle with the same identity, and rechecks the
pathname. The writer remains open until the read handle is bound, preventing
identity reuse of an unlinked original during that handoff.

`release/bootstrap/prepare.go:219` uses `destination.removeOwned` while retaining
the verified snapshot handle. Successful snapshot removal and failure cleanup
share the creation-identity check. Observed foreign replacements are preserved;
still-owned extracted state is removed through the retained root. The deferred
cleanup adds only a bounded authored error when output remains, without exposing
paths or underlying errors. Cancellation and existing destination/parent
substitution checks still pass.

The original independent `TestReviewSnapshotSubstitution` was copied unchanged
from the isolated `72ea5bb` review into an isolated `git archive f6d2906` copy.
It now passes: the archive-reader Close callback substitutes a distinct file
containing identical authenticated bytes, preparation refuses, and the replacement
remains. The two submitted regressions additionally verify retained identity and
bytes, absence of a completion receipt, truthful cleanup reporting, and cleanup
of extracted files when replacement happens immediately before owned removal.

An independent negative control replaced only `removeOwned` with unconditional
pathname removal in the isolated copy. The helper regression failed with
`removed a foreign replacement`. The mutation was restored, and the original
callback probe plus both submitted regressions passed again.

## Verification and limits

Independent checks on the isolated exact candidate:

```sh
go test -race -count=1 ./release/bootstrap ./cmd/minecraft-ui-bootstrap ./internal/selfupdate
go vet ./release/bootstrap ./cmd/minecraft-ui-bootstrap ./internal/selfupdate
GOOS=windows GOARCH=amd64 go test -exec /bin/true ./release/bootstrap ./cmd/minecraft-ui-bootstrap ./internal/selfupdate
```

All passed; race package times were bootstrap `1.821s`, command `1.015s`, and
selfupdate `10.690s`. Focused callback/removal, cancellation, latest-failure and
destination/parent binding tests also passed. `git diff 9ee62da f6d2906 --check`
passed. The submitted report's R1 explanation and test claims match the reviewed
change and independently observed behavior.

The checked Go 1.27 Windows `os.Root` implementation opens files with
`FILE_SHARE_DELETE` and supports retained-handle deletion; the correction does
not inadvertently use the ordinary Windows `syscall.Open` sharing behavior.
This inspection and successful cross-compilation are source evidence only.
Native Windows DACL/reparse/substitution and command execution remain pending.

Publication remains receipt-last under a caller-controlled parent. Identity
comparisons do not create an atomic compare-and-unlink primitive against a
concurrent writer with the same identity. Initial authentication of the preparer,
later privilege-crossing revalidation, outer installer integration, native
lifecycle, and release qualification remain separate obligations. No production
source, staging, commit, push, local native service/account/firewall, game, or
preview mutation was performed by this review; only this report was added to
the shared repository.

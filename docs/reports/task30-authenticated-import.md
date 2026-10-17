# Task30L1A authenticated prepared input import

Status: scoped source implementation accepted by the integrator. This package only
reauthenticates and copies prepared inputs into a private transaction child. It
does not install, invoke, or otherwise execute any imported file.

## Implemented boundary

`internal/installinputs` exposes the frozen `Import`, `Summary`, `Members`,
`OpenMember`, and `Close` API. Import clones the independently supplied trust,
requires an exact version and native Linux/Windows amd64 target, verifies the
original bootstrap metadata and signature with `candidate.VerifyBootstrap`,
and requires explicit opt-in for `DEVELOPMENT ONLY`. The completion receipt is
bounded, rejects unknown and duplicate fields, and is compared against the
authenticated manifest and original metadata digest.

The source directory must contain exactly the three prepared metadata files
and the authenticated target members. Enumeration reads at most the frozen
maximum (17 entries) plus one overflow sentinel for both source and private
destination directories. Files are opened relative to a checked root,
retained while copying, and checked for identity, single-link status, size, and
digest. Streamed source and destination digest rechecks use a fixed 128 KiB
buffer, stop on context cancellation between reads, enforce the expected size
as a byte limit, and rewind the handle on exit. Imported outputs use exclusive
root-relative creation; the actual creation handles stay open as identity
pins. Metadata bytes are copied unchanged with mode 0600. Member bytes and
signed modes are checked before success. Destination and source sets are
rechecked, files and Linux parent directories are synced, and errors preserve
`context.Canceled` identity through wrapping while presenting fixed stage
labels.

`OpenMember` accepts only an exact signed member name. It opens the private
copy root-relative, compares the opened file with the retained creation pin,
rehashes the opened handle against the signed size and digest, rewinds it, and
returns that handle to the caller. `Close` closes pins and the root while
leaving imported files for the future transaction journal.

Failure cleanup removes only paths still bound to the retained creation
handles. It preserves replacements and foreign contents and reports incomplete
cleanup when an owned path or the created `inputs` directory is missing or has
changed identity. File pins close after deletion attempts and before the
private child directory is removed. Unix requires a root-owned production
transaction root with no group/world permissions. Windows checks the protected
Administrators/System DACL and owner policy and creates the `inputs` directory
with a protected administrative DACL; files inherit that ACL. The Windows
parent-directory durability helper is intentionally a no-op and requires a
separate native qualification review.

## Verification

Focused tests use ephemeral Ed25519 keys and synthetic signed candidate
metadata. They cover exact byte preservation, Linux and Windows member layouts
through the internal platform seam, wrong-key/repository/version/expiry and
same-size digest bypass rejection, explicit development opt-in, inconsistent,
duplicate, and unknown receipt rejection, existing destination refusal,
cancellation identity and cleanup, symlink and hardlink rejection, missing
and extra prepared entries, production transaction-root private-mode rejection
and non-root-owner rejection when running as an unprivileged user, bounded
directory enumeration including its overflow sentinel, and cancellation of a
large digest recheck after one buffer while preserving the ordinary digest
control. They also cover prepared-source path replacement, OpenMember rejection
of a replaced private path, private-copy mutation rejection, and cleanup error
propagation while preserving missing/replaced transaction children and foreign
contents. Injected post-create failures verify same-identity empty directories
are cleaned and replacements are preserved with an incomplete-cleanup stage. A
successful `Close` test checks every retained creation pin is
closed while the imported private files remain in place; cancellation tests
check that partial outputs are removed.

Commands passed:

```text
go test ./internal/installinputs -count=1
go test -race ./internal/installinputs -count=1
go vet ./internal/installinputs
GOOS=windows GOARCH=amd64 go test -c ./internal/installinputs -o /tmp/installinputs.test.exe
```

The Windows command is a compile check only. No native Windows test or DACL
qualification was performed. This report does not claim installer integration,
installation lifecycle completion, or Task30 acceptance; those require root's
independent review and the separately owned native gate.

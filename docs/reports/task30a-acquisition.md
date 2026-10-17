# Task30A1 bootstrap acquisition source evidence

Status: implementation submitted for independent review; this report does not
complete Task30 or qualify an installed release. Evidence recorded 2026-10-17
with Go 1.27.0 on Linux amd64. No dependency, candidate format, producer, workflow,
native installer, service, account, firewall, game, existing database, or preview
was changed by this packet. No input executable was run.

Review correction R1 is submitted below against reviewed candidate `72ea5bb`.
Independent re-review remains required before accepting this packet.

## Interfaces and command

`release/bootstrap.Prepare(ctx, Options, Acquisition) (Receipt, error)` is the
typed entrypoint. `Acquisition` has `Latest`, `BootstrapMetadata`, and
`BootstrapArchive`; the shipping `selfupdate.Source` satisfies it. Passing nil
selects that fixed public GitHub source, or the checked local directory selected
by `Options.Artifacts`. Library-only platform/clock injection permits both
format1 bundles and validity-window failures to be tested deterministically.

The native command accepts exactly:

```text
minecraft-ui-bootstrap prepare --repository OWNER/REPO --trust-file ABSFILE --version latest|vMAJOR.MINOR.PATCH --output ABSNEWPATH [--artifacts ABSLOCALDIR] [--allow-development]
minecraft-ui-bootstrap version
```

The command fixes the platform to its own Linux/Windows amd64 runtime and uses
real UTC. There is no URL, credential, proxy, HTTP transport, or clock override.
Offline artifacts require an exact version. Preparation has a ten-minute total
context deadline and interrupt/SIGTERM cancellation. `version` prints exactly
`minecraft-ui-bootstrap <version>\n`; build stamping uses `-X main.version=...`.
The default version is `0.0.0-dev`.

The new `Source.BootstrapMetadata` and `Source.BootstrapArchive` expose only
`bootstrap.json`, `bootstrap.sig`, and the exact Linux/Windows amd64 bootstrap
archive names. Their common private request routine retains the shipping fixed
HTTPS transport, public-address DNS validation, redirect/credential limits,
timeouts, and authored errors. The existing updater asset allowlist is unchanged.
`latest` uses shipping `Source.Latest`, then binds that exact stable version for
all metadata/archive requests. Actual 404 remains a distinct no-release failure;
network failures remain failures. Unsigned titles/notes/URLs are never emitted by
the preparer.

## Authentication and filesystem behavior

Repository, version, platform, and absolute clean path syntax are validated
before filesystem/network I/O. Public trust is independently supplied, bounded
to 16 KiB, read from a regular single-link file with retained ancestry and opened
identity checks, and parsed through `candidate.ParsePublicTrust`. Symlinks and
Windows reparse points in checked ancestry are refused. Metadata authentication
uses `candidate.VerifyBootstrap` unchanged, including exact canonical signature,
repository/version, expiry, platform, size, mode, and member allowlists.
Authenticated `DEVELOPMENT ONLY` metadata is denied unless explicitly requested.
That option grants no release qualification or host mutation.

The output parent is opened component by component from its volume root; every
ancestor handle and observed identity is retained. An existing directory, file,
or dangling-link destination is refused. The destination is exclusively created
with Unix 0700, or a protected Windows DACL allowing only the current process
identity, administrators, and SYSTEM with inheritance to child files.

The bounded downloaded/local archive is copied to a new private `.bundle` file.
The preparer retains an opened snapshot and calls `candidate.VerifyBundle` on
that snapshot before extraction. Extraction reads the same retained bytes,
rechecks regular type, ordered exact name, mode, size, and SHA256, and creates
only the authenticated fixed member names exclusively through the retained root.
It never passes an archive-supplied name to an absolute OS operation, invokes a
general extraction command, or executes an input. Unix output modes match the
signed contract exactly. Windows checks archive contract modes and inherits
private filesystem rights from the protected parent. The unchanged candidate
verifier additionally creates/removes its own bounded temporary verification
spool; it never extracts or activates members.

`completion.json` is written last and synced. It includes validated repository,
version, schema, platform, provenance, manifest SHA256, and every member hash,
size, and mode. The same bounded JSON is the only successful preparation stdout.
Errors contain only authored stage labels, never raw file paths, remote bodies,
signed URL query strings, or unvalidated metadata. Failure/cancellation removes
owned files by checked identity through the retained destination root; replacement
output paths are not traversed or recursively removed. A renamed original
destination can remain empty when its name was replaced; preserving the foreign
replacement takes precedence over removing that original directory name.

Publication is **not an atomic directory rename**: an observer can see a partial
destination while the command runs. Consumers must wait for successful command
exit and require the completion receipt. The parent is caller-controlled;
identity comparisons and pathname removal are not an atomic compare-and-rmdir
primitive against a malicious concurrent writer operating as that same identity.
Directory replacement is detected at each create and final publication check,
and all member operations remain anchored to the original retained root. As with
the existing candidate verifier, later privilege crossings require revalidation
or an immutable retained checked source; a completed user-writable directory is
not authority to execute files as administrator.

## Deterministic evidence

Focused tests construct exact format1 USTAR/gzip and ZIP bundles containing all
14 Linux and 5 Windows members and authenticate metadata with genuine ephemeral
Ed25519 signatures. No external network is used. Tests cover:

- Exact pinned/latest acquisition and requested-version binding for both OS
  bundles; exact Source request paths through the private transport test seam.
- Repository/version/platform/expiry/future/signature/public-trust/duplicate-JSON
  rejection, development default denial and explicit opt-in, oversized metadata.
- Authenticated extra, traversal, absolute, symlink, duplicate, reordered,
  corrupt, oversized, wrong-member-hash, appended-tail, and archive-size cases.
- File/directory/dangling output refusal, trust/parent symlinks, trust hardlinks,
  destination replacement, parent substitution, cleanup binding, cancellation,
  latest404 versus network failure, exact extracted hashes/modes, and receipts.
- A real invocation of the shipping preparation command against local signed
  fixtures with current validity. Its Linux manager member is an executable
  marker-creating shell fixture: the expected marker remains absent, demonstrating
  that preparation leaves the member as data.
- Small command-wrapper cases: exact version stdout, bounded/secret-free argument
  and trust errors, no stdout on failure, and absence of a clock override.

Meaningful mutation evidence was run and restored before the final green run:

1. Temporarily bypass development opt-in enforcement:
   `TestAuthenticationRefusalsLeaveNoOutput/development_default_denied` fails with
   `accepted invalid authentication`.
2. Temporarily add `bootstrap.json` to the shipping update allowlist:
   `TestBootstrapSourceExactPathsAndUpdateAllowlist` fails with
   `bootstrap widened shipping update allowlist bootstrap.json`.

Verification commands:

```sh
go test -race -count=1 ./release/bootstrap ./cmd/minecraft-ui-bootstrap ./internal/selfupdate
go vet ./release/bootstrap ./cmd/minecraft-ui-bootstrap ./internal/selfupdate
GOOS=windows GOARCH=amd64 go test -exec /bin/true ./release/bootstrap ./cmd/minecraft-ui-bootstrap ./internal/selfupdate
git diff --check
go run ./cmd/minecraft-ui-bootstrap version
```

The focused race suite, vet, diff check, native version command, and Windows
cross-compilation pass. `/bin/true` explicitly skips running the Windows test
binaries: this is compilation evidence only. Windows private-DACL behavior,
reparse/substitution behavior and native command execution still require native
host evidence; cross-compilation makes no qualification claim.

## Files and remaining integration

Owned files are `release/bootstrap/{prepare,files,files_unix,files_windows,extract}.go`,
`release/bootstrap/prepare_test.go`, `cmd/minecraft-ui-bootstrap/{main,main_test}.go`,
`internal/selfupdate/source_bootstrap{,_test}.go`, and this report. The permitted
shared change is six lines in `internal/selfupdate/source.go` factoring out the
already allowlisted private HTTP request body. Candidate format1 and producer
files are unchanged.

The integrator owns independent review, native bootstrap build receipts, exact
artifact/workflow verification, the release-specific outer installer pinning
these command bytes with an independently approved SHA256, independently reviewed
public trust, and native install/repair/upgrade/uninstall/rollback handoff. This
command cannot authenticate its own first download. No production signing key,
public release, installed lifecycle, game/client/restore, or stable qualification
is established by this packet.

## Review correction R1: bind the snapshot to its creation identity

The independent review in `docs/reports/review-task30a-acquisition.md` identified
a real ownership gap in candidate `72ea5bb`. The snapshot reopen checked the
current pathname against its own opened identity, rather than the identity
recorded by exclusive creation. The successful path also removed that pathname
without comparing creation identity.

Reproduction before the fix used the reviewer's retained isolated exact-candidate
probe:

```sh
cd /home/thim/.cache/dilla-tmp/task30a-review-72ea5bb
go test -run '^TestReviewSnapshotSubstitution$' -count=1 ./release/bootstrap
```

It failed as reported: `Prepare error=<nil>` and the replacement `.bundle` was
absent. The archive reader's Close callback had replaced the original snapshot
with a distinct regular file containing the same authenticated archive bytes.
This demonstrated ownership failure without any signature bypass.

The fix retains the original creation handle until a read handle has been
compared to the recorded creation identity. `destination.openOwnedRegular`
checks both the pathname and opened file against that identity; the read handle
remains open through verification, extraction, and ownership-checked removal.
`destination.removeOwned` checks the creation identity before deletion and is
shared by successful snapshot deletion and failure cleanup. An observed foreign
replacement is refused and preserved. Failure cleanup now reports a bounded
authored message when replacement or unremovable state remains; it never reports
full cleanup in that case. This does not add an atomic compare-and-unlink OS
primitive or change the previously documented caller-controlled-parent boundary.

Two deterministic regressions cover the finding:

- `TestSnapshotSubstitutionOnArchiveCloseRefused` reproduces the exact callback
  replacement before reopen. Preparation fails, the replacement retains its
  identity and bytes, no completion receipt appears, and the error reports
  retained output state.
- `TestSnapshotReplacementBeforeOwnedRemovalPreserved` verifies and extracts the
  original archive, then substitutes its pathname immediately before calling the
  same removal helper used by successful preparation. Removal refuses, cleanup
  preserves the replacement and removes all still-owned extracted files, and no
  completion receipt exists. Temporarily bypassing that helper's identity check
  makes this test fail with `removed a foreign replacement`; the mutation was
  restored before final verification.

The callback regression passes after the fix. Focused race tests, vet and Windows
cross-compilation were rerun with the commands above. Windows compilation remains
compilation evidence only; no local native lifecycle operation was performed.
The R1 changes are limited to `release/bootstrap/files.go`,
`release/bootstrap/prepare.go`, `release/bootstrap/prepare_test.go`, and this
report. No outer installer, native updater, producer, workflow, staging, commit,
or push is part of this correction.

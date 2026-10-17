# Release dependency notice evidence

This change supplies a reproducible dependency notice artifact for Task 24/25
integration review. It does not mark either package complete, publish anything,
or qualify a stable release or native Windows execution.

## Scope and inventory

The generated notice union contains 25 actually linked Go modules, the Go 1.27.0
toolchain component, 48 locked non-dev npm package entries, and two preserved
copied-source notice components. Go discovery uses both the manager and updater
entrypoints on Linux and Windows amd64 with CGO disabled. Standard/vendor
package ancestry contributes 11 Go license/patent files. Notice bytes are retained
intact; the UI list is a conservative superset of tree-shaken bundled code.
`modernc.org/libc` third-party notices, memory's multiple licenses, SQLite/SQLite
Vec notices, deployment attribution and map fixture attribution are included.

The missing `react-remove-scroll-bar` 2.3.8 license text is supplied by a checked-in,
SHA-256 pinned later upstream notice. Both generated artifacts explicitly state
that this text was absent from the old tarball and its release gitHead. See
`release/licenses/react-remove-scroll-bar/PROVENANCE.md` for the primary source,
commit, exact fallback hash and original copyright year. No dependency changed.

## Verification

- `make notices check-notices`: PASS. Actual generation and stale-artifact check
  succeeded offline; nine focused, network-free Python tests passed.
- An independent current `go list -mod=readonly -deps -f` invocation for each of
  Linux/Windows, with both release entrypoints, matched the generated 25 module
  identities and versions exactly.
- Two additional actual generator invocations produced byte-identical artifacts.
  Inventory inspection found no host paths. The inventory includes no timestamps.
- `git diff --check` on the owned tracked files: PASS.

Focused tests exercise scoped npm identity, exact version matching, missing/
empty/truncated license rejection, multiple top-level and inherited notices,
exact fallback version/checksum/provenance, both Go platforms and entrypoints,
network/toolchain-download disabling, original text byte preservation,
deterministic generation, absence of temporary host paths, and stale-check
failure without rewriting output.

Generated SHA-256 values at review handoff:

```text
2dea5c5edfc9df82e39fa645b6375c27f79e60a34a47fa006376161c86d18c03  THIRD_PARTY_NOTICES.txt
50b85138da895a72c5dba2bc45ca89907d1d1ab9472bb23fa135aaf7017bd08f  release/dependency-inventory.json
```

## Owned files and integration

Owned files are `release/generate_notices.py`,
`release/test_generate_notices.py`, `release/licenses/react-remove-scroll-bar/**`,
`THIRD_PARTY_NOTICES.txt`, `release/dependency-inventory.json`,
`docs/third-party-notices.md`, and this report. Narrow integration edits append
`notices`/`check-notices` Make targets and a `make check-notices` CI step after the
existing build/check command. Existing commands are preserved. No files are staged
or committed by this worker.

The generator has no added dependencies and performs no network fetches. Go
modules, the pinned toolchain and `npm ci` installed dependencies must already be
available. It validates local dependency identity and structural notice presence;
it does not rehash installed source trees against upstream archives or substitute
for review of legal obligations. Build flags/targets or dependency changes require
regeneration and notice review. No full application suites were rerun for this
artifact-only work. The evidence is a source/build-input inventory comparison,
not a claim of new native builds or runtime qualification. No publisher, keys,
helper package, release archive or stable support claim was introduced.

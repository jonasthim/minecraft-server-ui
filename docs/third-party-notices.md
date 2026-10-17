# Third party release notices

`THIRD_PARTY_NOTICES.txt` accompanies the manager release executable and the
project `LICENSE`. `release/dependency-inventory.json` records the deterministic
source inventory and hashes of the preserved legal text. The same notice union
also covers the separately installed updater helper. Generating notices does
not publish a release or qualify any operating system or Minecraft version.

Run from the repository root:

```sh
make notices
make check-notices
```

Generation needs Python 3.9+, the exact locally installed Go toolchain specified
by `go.mod`, downloaded locked Go modules, and `web/node_modules` populated by
`npm ci`. Provision dependencies separately (`make deps` can access the network).
Ordinary generation uses `GOPROXY=off`, `GOSUMDB=off`, and `GOTOOLCHAIN=local`; it
never downloads packages or a toolchain. It does not use external Python packages.
`make check-notices` runs focused offline validation tests and rejects stale
artifacts. CI runs it after its existing dependency installation/build checks.

The Go inventory comes from `go list -mod=readonly -deps -json` on both
`./cmd/minecraft-ui` and `./cmd/minecraft-ui-update`, for Linux and Windows,
amd64, CGO disabled, default architecture level v1, no build tags or experiments,
and no workspace overrides. Only non-main modules owning linked packages are
included, with their actual versions and module checksums. Test dependencies and
unused modules in the module graph are excluded. The inventory lists per-platform
import paths, including standard packages under the Go toolchain component.
Changes to release entrypoints, targets, or build flags require matching generator
review. Cross-platform source inventory is not native Windows qualification.

The UI inventory conservatively includes every lockfile v3 package entry without
`dev=true`, including transitive runtime dependencies. Tree shaking can omit code
from some of these packages. Each installed package name, version and license
declaration must match the lockfile; lockfile integrity is recorded. The generator
expects dependencies installed by `npm ci`; it does not independently rehash every
installed source file against its npm tarball or Go module archive.

The generator includes all top-level LICENSE/LICENCE/COPYING/COPYRIGHT/NOTICE,
UNLICENSE and PATENTS files, with suffixes, and legal files inherited through
linked Go package directories. This preserves, among others, modernc libc's
`LICENSE-3RD-PARTY.md`, memory's multiple licenses, SQLite/SQLite Vec notices, and
Go's root and linked vendor licenses/patent grants. It copies the legal text bytes
intact, with labeled separators. Some upstream text intentionally retains CRLF
line endings. Scoped `.gitattributes` rules disable text normalization for the
notice artifact, deterministic inventory, hash-pinned fallback license, and the
raw inputs hashed/copied into the artifacts (`go.mod`, `go.sum`, the npm lockfile,
Linux deployment notice and map fixture license), so
`core.autocrlf=true` cannot change their bytes. The notice file permits CR at line
end in Git whitespace checks while retaining the ordinary trailing-space,
end-of-file blank-line and space-before-tab checks. It rejects absent, empty, binary or very short
license files; those structural checks do not replace review of copyright and
license obligations when dependencies change.

`deploy/linux/NOTICE` preserves the MIT notice for supervision patterns adapted
from valheim-server-ui; attribution also remains in the source file headers.
`internal/mapdata/testdata/LZ4-LICENSE.txt` is included conservatively even though
test fixtures are not embedded release assets. Internal test/artifact directories
are never scanned for dependencies. No host paths, generation times, credentials,
or private runtime data are recorded.

One explicit fallback supplies missing text for `react-remove-scroll-bar` 2.3.8.
The package declares MIT but did not ship the text. The notice comes from a later
pinned upstream commit and retains its original 2025 copyright. Its provenance is
visible in both generated artifacts and
[the fallback record](../release/licenses/react-remove-scroll-bar/PROVENANCE.md).
It must not be described as having come from the old npm tarball. Generation
checks its exact version, MIT declaration and SHA-256; it does not fetch the URL.

Generated notices concern this application's bundled dependencies. Minecraft
servers, Java runtimes, mods and plugins downloaded separately have their own
licenses and are not redistributed in the manager archive. The archive contract
remains the executable, project `LICENSE`, and `THIRD_PARTY_NOTICES.txt`; helper
packaging and a production publisher require separate review. No stable release
qualification follows from this notice artifact.

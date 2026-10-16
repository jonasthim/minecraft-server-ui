# Shared checked directory bootstrap

Base: `e302b67`. Status: implementation frozen for independent integration review.
The integrator owns acceptance and the implementation ledger.

## Correction and contract

`internal/safepath.OpenRoot(ctx, absoluteCanonicalPath) (*os.Root, error)`
anchors at the filesystem root or a local Windows drive and descends one
component at a time. Each child is checked with rooted `Lstat`, `OpenRoot`, opened
identity and the parent's current entry. Symlinks, Windows reparse points,
nondirectories, noncanonical paths and UNC/device roots are refused. Intermediate
handles close on success; opened handles close on failure/cancellation. Wrapped
host errors and joined cleanup errors retain their `errors.Is`/`errors.As` causes.

The returned terminal handle pins its directory. It does not assert that its
absolute name still refers to that directory after acquisition. Feature-specific
retained ancestor checks remain the caller's responsibility. The reviewed
map/world/player implementations were not edited.

Adoption covers the assigned initial roots:

- Instance create, update, delete, materialization and deletion recovery in
  `internal/instance/service.go` and `reconcile.go`. First-use creation of a missing
  configured data directory remains supported, with one-component mkdir beneath
  checked parents. The instance parent is created beneath a checked data handle.
- Lifecycle sealing durability and activation removal in
  `internal/lifecycle/service.go`.
- Console history capture and legacy cursor acquisition/polling in
  `internal/console/history.go` and `legacy.go`.
- JAR and loader publication/verification in
  `internal/server/providerutil/{install,verify}.go` and
  `internal/server/loaderutil/{publish,verify}.go`. Each opens the derived canonical
  server or publication directory directly, checking ancestors below `dataDir`
  as well as those above it. This refuses redirection to another instance within
  the same data directory.

Existing `Paths` normalization continues to accept relative provider configuration;
canonical paths derived by `Paths.For`/`Resolve` reach the shared helper. Unix legal
configured directory names remain usable. Missing console log directories still
produce empty capture through a wrapping-aware `errors.Is(err, os.ErrNotExist)`
check. Missing cursor/materialization/activation roots retain their host causes.

The integrator additionally delegated the private context migration:
`console.newRawCursor(ctx, paths, id)` and
`lifecycle.removeActivation(ctx, id)`. Only their narrow callers changed in
`console/session.go`, `console/control_test.go`, and `lifecycle/operations.go`.
Save-release cursor acquisition uses the independent release operation context.
Backup-owned callers and session/activation adoption are the backup worker's
separate changes.

## Deterministic evidence

The added tests use local synthetic files and explicit substitution at the
post-validation/pre-bootstrap boundary; no network or timing race is needed.

| Surface | Evidence |
| --- | --- |
| Shared helper | Previously validated data, instances, instance and server ancestors are replaced with symlinks; the former absolute open demonstrably reads the outside fixture while the helper refuses a usable root. |
| Instance creation | Substitution refuses creation before an outside instances directory or SQL row is created. Missing configured directory creation still succeeds. |
| Console | History, cursor acquisition and polling refuse substituted log roots; no outside history persists. Missing log directory capture remains empty and a missing cursor retains `os.ErrNotExist`. |
| Lifecycle | Substitution leaves the outside activation file byte-identical. Missing state directories retain `os.ErrNotExist`. |
| Providers | Both publication and verification refuse data-root substitution and instance redirection to a sibling inside dataDir. No outside manager tree or loader builder is admitted. Both relative provider configuration cases still publish successfully. |
| Host/lifetime | Normal reads, pinned reads after rename, missing `*os.PathError`, closed-parent `os.ErrClosed`, pre-cancellation and cancellation during descent are asserted. |
| Windows | Drive/UNC/device and reparse-attribute policy tests compile in the native Windows source set. Native Windows execution remains unqualified. |

A diagnostic Go compile overlay replaced only the exported bootstrap implementation
with `ctx.Err()` followed by `os.OpenRoot(absoluteCanonicalPath)`. Every
substitution test group in all six scoped packages turned red, including the
within-data provider cases and all three console cases. Source files were not
mutated by that diagnostic. Overlay artifacts used for the run are
`/tmp/path-bootstrap-overlay.json` and `/tmp/path-bootstrap-unsafe-root.go`.

## Verification commands and results

All commands ran from the repository root against the shared working tree.
Package lists are deliberately scoped to this correction.

```sh
go test ./internal/safepath ./internal/instance ./internal/lifecycle ./internal/console ./internal/server/loaderutil ./internal/server/providerutil
```

PASS: all six packages.

```sh
go test -race ./internal/safepath ./internal/instance ./internal/lifecycle ./internal/console ./internal/server/loaderutil ./internal/server/providerutil
```

PASS: all six packages; final run reported approximately 1.0–6.7 seconds/package.

```sh
go vet ./internal/safepath ./internal/instance ./internal/lifecycle ./internal/console ./internal/server/loaderutil ./internal/server/providerutil
```

PASS: exit 0, no diagnostics.

```sh
mkdir -p /tmp/minecraft-path-bootstrap-windows
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/minecraft-path-bootstrap-windows ./internal/safepath ./internal/instance ./internal/lifecycle ./internal/console ./internal/server/loaderutil ./internal/server/providerutil
```

PASS: six Windows amd64 test binaries compiled. This is compilation evidence only.

```sh
go test -overlay=/tmp/path-bootstrap-overlay.json -run 'Reject.*Substitution' ./internal/safepath ./internal/instance ./internal/lifecycle ./internal/console ./internal/server/loaderutil ./internal/server/providerutil
```

Expected FAIL: exit 1; substitution groups in all six packages failed under the
unsafe bootstrap implementation.

```sh
git diff --check
```

PASS: no whitespace diagnostics.

No dependency changes, game execution, EULA acceptance, host service operations,
staging or commits were performed. Real Minecraft/version-layout qualification,
retained-chain behavior outside this correction and native Windows execution
remain separate acceptance work.

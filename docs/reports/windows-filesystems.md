# Windows filesystem fixture corrections

2026-10-17. Scope: mapdata, players, world, loaderutil and providerutil. Baseline hosted run: `39022162348`, commit `98540a2`, Windows Server 2022 x64, Go 1.27.0. Native Windows rerun is still required. No service, game/client or release qualification is claimed.

## Causes and changes

### Retained ancestor handles

The hosted log shows `Access is denied` when `TestSourceRetainsAndRechecksAncestorAfterOpen`, `TestPlayerServerRootPinsAndRechecksEveryAncestor` and `TestOfflineActivationRechecksAncestorAfterStaging` try to rename an ancestor after production code has opened the descendant tree. The last test reports `swapped=false` because the rename was prevented, before its substitution flag could be set. This is a prevented attack, not proof that an outside path was accepted.

These tests now accept only a Windows permission denial at that exact attempted rename. They require the destination to remain absent, the original pinned tree to verify, and the pinned reads to return the original data. Offline activation must still fail with the original denial, leave both saved lists unchanged, and remove staging. Other rename errors still fail. When rename succeeds, the original substitution/rejection assertions still run. No platform skip was added.

The observation should not be attributed to missing delete sharing on every root handle: Go's child `Root.OpenRoot` handles use `FILE_SHARE_DELETE`. Ordinary `OpenRoot` acquisition uses `syscall.Open`; the held descendant-tree case must be distinguished from a single open directory. Relevant primary source: [Go root implementation](https://go.dev/src/os/root_windows.go), [Go Windows relative operations](https://go.dev/src/internal/syscall/windows/at_windows.go), and [Microsoft handle sharing rules](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-createfilew).

### Relative configuration fixtures

The two `TestPublishRetainsRelativeDataDirectoryNormalization` failures attempted `filepath.Rel` from the checkout on `D:` to temporary fixtures on `C:`. That is not a representable relative path. Each test now uses `t.Chdir` to the parent of its temporary data directory before constructing the relative configuration; cleanup restores the working directory. The real publisher still has to succeed and return an absolute working directory. [Go documents that Rel can return an error when the target cannot be represented relative to the base](https://pkg.go.dev/path/filepath#Rel).

### World names and collisions

The original capture test assumed `os.WriteFile` created exactly the requested name. Its Windows failures were `CON.txt`, `trailing.` and `Case.dat`/`case.dat`. A disposable Wine probe demonstrated three relevant representations: `CON.txt` failed without creating a file; `trailing.` enumerated as `trailing`; and both case variants addressed one file. The hosted baseline contains no directory enumeration, so the precise native representation is an inference until the revised fixture runs there.

The fixture now enumerates before/after entries. Represented unsafe names and two distinct case variants must still fail capture. The narrowly recognized alternatives require proof of no new device-name entry, a normalized trailing-dot file with exact bytes, or two case paths referring to the same file identity. Successful captures of those safe native states must contain exactly one normalized file with its actual bytes and must not invent reserved/trailing-dot archive paths. Unexpected states fail.

`TestPortableWorldPathValidation` independently exercises production `safePart` rejection of reserved, trailing, control, traversal, separator and stream syntax, plus exact/case-fold path collisions. The existing inventory collision block was extracted unchanged into `portablePaths.add` and remains on the actual capture path. These checks run even on filesystems unable to create the unsafe fixture. No production validation policy changed. [Microsoft documents reserved device names, trailing-dot limitations, namespace parsing and default case insensitivity](https://learn.microsoft.com/en-us/windows/win32/fileio/naming-a-file).

## Verification

- The hosted baseline provides the failing Windows evidence at `source_test.go:214`, `root_test.go:106/170`, `safepath_test.go:102/86`, and `archive_test.go:417`.
- Fresh Linux `go test ./internal/mapdata ./internal/players ./internal/world ./internal/server/loaderutil ./internal/server/providerutil -count=1 -timeout=180s`: PASS.
- Fresh Linux `go test -race` for those five packages with `-count=1 -timeout=180s`: PASS (mapdata 2.597s, players 18.863s, world 26.807s, loaderutil 1.524s, providerutil 1.455s).
- Scoped `go vet`: PASS.
- Fresh repository `go test ./... -count=1 -timeout=180s`: PASS. This ran against the shared working tree, including other workers' changes.
- Mutation proof: temporarily removing `strings.ToLower` from the extracted collision registry made `TestPortableWorldPathValidation/collision-case.dat` and `/collision-CASE.DAT` fail; restoring it made the focused test pass.
- All five test binaries cross-compiled for Windows amd64 with `CGO_ENABLED=0`. This establishes compilation only.
- `git diff --check` for owned files: PASS.

## Wine diagnostic limits

Wine used only a disposable task-specific prefix under `/home/thim/.cache/dilla-tmp/windows-filesystem-fixtures/wine`; no dependency installation or user Wine profile was used. Focused world normalization/validation, both relative publisher tests, mapdata retained-root and player pinned-read tests passed. **Offline activation staging cleanup failed under Wine**, and its assertion remains enabled.

The cleanup difference was reproduced without application code: create `instance/server/stage.tmp`, hold `os.OpenRoot(server)`, rename `instance` to `instance.held`, symlink the old path to a different tree, then call `root.Remove("stage.tmp")`. Wine returns nil while the file in the held tree remains present, even after `root.Close`. The same probe on Linux removes it. Hosted Windows instead denied the ancestor rename before this state. This diagnostic is not evidence of successful native Windows cleanup, nor justification for weakening the cleanup check or changing production code for Wine.

The integrator must review this change and run hosted native Windows tests before claiming this failure set resolved. SQLite URI failures and other native subsystems are owned separately. No ledger completion, staging or commit is performed by this worker.

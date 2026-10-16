# Task14 — archive, retention and explicit restore

Implementation submitted for independent review; no package acceptance/ledger claim is made by the worker. Companion lease evidence: `task13.md`. Frozen exported API, recovery and consumer contract: `.superpowers/sdd/2026-10-16-minecraft-server-ui/task13-14-contract.md`.

## Archive and privacy

World roots come from validated `world.Service.Discover`, including associated Paper/custom dimensions and ambiguity rejection. Backup capture does not call the live world-export service or reacquire ordinary idle admission. Full scope includes local addon data/config and ordinary hidden saves. Manager database/sidecars, launcher controls, .manager/private artifacts and the root backup tree are excluded; symlinks, hardlinks, reparse destinations and special files are refused. External databases are not captured.

Filesystem capture anchors the local volume/filesystem root and retains checked component handles, uses bounded entry/depth/path/file/total byte limits, checks file identity/change and rereads hashes. Linux opens use nofollow/nonblocking with one-link verification; Windows uses reparse/link-count checks. Stages remain outside the source server tree.

Full ZIPs remain private local files, including ordinary addon credentials needed for restore. Operator download returns the same validated manager-owned archive and discloses private contents. Viewer metadata and audit do not include configuration content. Decoded Java properties share the instance continuation/unescape parser: management-server-* and private RCON controls are removed, while ordinary settings remain. No uploaded archive or arbitrary host path endpoint exists.

Manifest includes instance, Minecraft/flavor/loader/runtime/layout/active level-name, stopped-versus-live capture mode, selected roots, saved-world presence, file sizes/SHA-256 and consistency/preservation disclosures. Validation requires expected entries, configured level.dat when a world was captured, regular entries, no duplicate/case/ancestor collisions, portable paths, strict bounds and matching hashes. Output is validated and synced before publication; renamed output identity is checked before SQL ready state. Interrupted capture/publication never becomes usable by assumption.

## Retention and restore

Automatic defaults are ten unprotected automatic archives or thirty days, evaluating either expiration rule. Manual and safety archives are protected. Active downloads, incomplete restore references and queued/running restore requests prevent deletion. Deletion is journaled for restart retry. Historical restore IDs remain history without permanently pinning expired automatic archive rows through foreign keys.

Restore requires explicit confirmation, exact instance/Minecraft/flavor/loader/layout/active level-name compatibility, verified stopped native admission and successful protected full safety backup. Safety failure prevents target mutation. Full restore replaces the union of archived/current included top-level roots; world restore replaces selected world roots. Manager/runtime metadata stays out of that scope.

Restore also refuses unresolved state/config.pending.json, lifecycle activation rows and independent lifecycle boot markers before safety capture, and rechecks that precondition during startup forward recovery. This prevents later config recovery from overwriting newly restored properties. TestRestoreRejectsUnresolvedConfigAndLifecycleRecoveryBeforeSafety verifies all three cases and unchanged target data.

Current typed manager settings, ports, EULA, version/runtime selection and private management credentials are preserved. Original stopped-session `MaterializeRestoredProperties` restores allowed persisted unknown `Config.Extra` changes/deletions with a revision guard, commits SQL before file materialization and replays forward after interruption.

A synced `state/backup-restore.json` boot blocker precedes the first rename. Independent launcher startup checks this separately from lifecycle activation. Every root move records old/new content digests and intent before replacement; old data moves into owned preimages, then staged new data moves into place without overwrite. Linux uses renameat2 relative to pinned parent handles, Windows an NT relative directory-handle rename. Staged directories are synced before durable replacement intent. Recovery resumes an already-confirmed restore forward only when stage/current/preimage state is unambiguous. Changed provenance or ambiguous content retains marker/journal/preimages for repair. There is no automatic world rewind or automatic preimage pruning.

## Backend evidence

Real filesystem/SQLite tests cover successful live/stopped capture, archive corruption/traversal/link/path/collision denial, hidden saves, private properties removal, local addon inclusion, configured save marker, both retention limits/manual protection, active export/queued restore retention protection, confirmation/stopped/version gates, failed safety backup and interruptions after old move/new move/before materialization. Interrupted restore is followed by a new paused jobs service and original stopped recovery capability; launcher remains blocked until forward recovery succeeds. Copy/expired capability misuse and persisted Extra changes/deletions are checked.

Successfully run commands during this task:

```
go test ./...
go vet ./internal/backup ./internal/instance ./internal/console ./internal/launcher ./internal/api ./cmd/minecraft-ui
go test -race ./internal/backup ./internal/instance ./internal/console ./internal/launcher ./internal/api ./cmd/minecraft-ui
GOOS=windows GOARCH=amd64 go test ./internal/backup ./internal/instance ./internal/console ./internal/launcher ./cmd/minecraft-ui -run '^$' -exec /bin/true
```

HTTP tests `TestBackupHTTPRoleMatrixAndStrictInputs` verify viewer metadata/operator mutation/export gates and strict inputs. No arbitrary upload endpoint is mounted.

## 390 px browser evidence

In-app Browser bootstrap returned `No browser is available`; the explicitly authorized isolated Playwright fallback was used. Reproducer: `.superpowers/sdd/2026-10-16-minecraft-server-ui/backup-browser.mjs`. Artifacts are in its `backup-browser-artifacts` sibling directory.

Primary shipping embedded binary, fresh manager DB, real native boundary: first admin setup → instance creation through shipping UI with EULA unchecked → visible backup link → empty page → actual queued backup failure while native systemd service is unavailable → viewer/operator accounts via shipping API and actual role UI login. No horizontal overflow or page errors at 390×844. Artifacts: `shipping-empty.png`, `shipping-failure.png`, `shipping-viewer.png`, `shipping-operator.png`.

Supplementary `TestBackupBrowserHarness` uses real auth/instances/jobs/archive/restore/filesystem with only the native stopped boundary substituted. Domain state is created through shipping UI/API, never DB-seeded. It verifies world-backup failure before a world exists, stopped full-backup success, immediate validated-list refresh, actual ZIP download, disabled restore until confirmation, protected safety backup/restore success, and viewer/operator controls. Artifacts: `fixture-ready.png`, `fixture-restore-confirmation.png`, `fixture-restored.png`, `fixture-downloaded.zip` plus empty/failure/role screenshots. This exposed and fixed a terminal-poll effect cancellation bug before submission.

Earlier web build/typecheck/lint passed. A later rebuild/typecheck was temporarily blocked by concurrently introduced map schemas/Node test placement, reported to root; Vite production bundling independently succeeded for the browser-tested backup UI. Root then supplied merged map types and corrected test placement. Final npm --prefix web run build (including TypeScript) and npm --prefix web run lint both passed. Final full Go suite, scoped race/vet and Windows cross-compilation also passed after the restore/config precondition regressions.

The opt-in browser harness processes were intentionally interrupted after browser assertions completed; their process interruption is not a unit-suite result. Normal go test ./... skips that explicitly opt-in harness and passed.

## Remaining qualification

Real Minecraft version/layout combinations and Linux/Windows native archive/restore/watchdog workflows are not qualified by these fixtures/browser tests. No EULA, game or service installation occurred. Windows rename/directory durability remains native-host qualification work. Preimages are intentionally retained for reviewed cleanup and may use extra disk space. Scheduler/update consumers are separate Tasks18/19 using the frozen admitted backup primitives. The integrator must review evidence and update the ledger before acceptance.

## Fix round 1/5 — Important export finding and assigned shared-parser follow-up

Review basis: `17e7a50` (current task dispatch HEAD `4cb6e0e` included unrelated map changes). The Important finding was reproduced before the fix: a line such as `# ordinary comment \` followed by `management-server-secret=private-value` was retained by `BackupProperties`, so a validated archive could include that manager credential. Java natural comments terminate at newline regardless of trailing backslash.

`BackupProperties` now recognizes natural `#`/`!` comments, after optional property whitespace, before counting continuation backslashes. It preserves the existing comment bytes and ordinary configuration. Lines belonging to a genuine continued property are not reclassified as comments, so literal `#`/`!` value text stays intact and continued private values remain fully omitted.

`TestBackupPropertiesCommentBackslashDoesNotHidePrivateControls` failed before the fix and passed afterward across 60 combinations: both comment markers, leading property whitespace, every known management/RCON control family, future-prefixed controls, escaped keys and continued keys. `TestBackupPropertiesCommentMarkersInsideContinuedValuesRemainData` verifies ordinary continued values are preserved and private continued values containing these markers are removed.

Separately from the review finding, the worker inspected shared `unknownPropertiesAt` and reproduced ordinary-property loss after the same two comment markers with a temporary Go overlay. Root explicitly assigned a narrow `properties.go` follow-up and new `properties_comment_test.go`. The shared parser now skips natural comments before continuation handling. New regressions fail on the prior parser and pass with the fix, proving the following ordinary property survives, continued values beginning `#`/`!` remain data, and legitimate key/value continuations and escaped separators retain their decoded values.

Fix-round verification, limited to the affected instance/backup packages while other workers are active:

```
go test ./internal/instance -run '^(TestBackupProperties|TestUnknownProperties)' -count=1
go test ./internal/instance ./internal/backup -count=1
go test -race ./internal/instance ./internal/backup -count=1
go vet ./internal/instance ./internal/backup
```

Focused red/green, scoped normal tests, scoped race tests and vet all passed. No main/App/launcher changes, dependencies, staging, commits or real game/native qualification were made in this round. Independent scoped rereview and integrator ledger acceptance remain required.

# Task 12 — configured world discovery and download

Date: 2026-10-16. Implementation submitted for integrator review. Package acceptance remains pending integrated browser evidence and real saved-world/platform qualification; the integrator owns the ledger. No staging or commits were performed.

## Implemented behavior

`world.New(db, instances, paths, queue)` exposes `Discover(ctx, instanceID)`, `Capture(ctx, actor, instanceID) (*Archive, error)`, and `ReconcileExports(ctx) error`. Archive implements Read/ReadAt/Seek/Close and provides Size, SHA256 and Filename. The configured level name comes from the instance service. All filesystem roots derive from instance.Paths; responses contain relative paths only.

Discovery distinguishes absent, present, ambiguous and unsupported saves. It observes legacy Vanilla/Fabric/Forge layouts, Paper's associated `_nether`/`_the_end` roots, 26.1 namespaced standard dimensions and player directories, namespaced custom dimensions and conservative unknown mod dimensions. It reports configured version/flavor plus observed-layout provenance. Mixed legacy/new layouts refuse capture. This is directory discovery, not NBT/region decoding or proof of server compatibility.

Capture holds queue.WithIdle during inventory/copy/validation/audit, allowing an already-running JVM. The reservation ends before the HTTP transfer. Source identity/size/mtime and a second digest pass detect changing files, including same-size writes whose timestamp is restored; replacement links and unsafe portable paths fail. The copy is explicitly best effort and never acquires a save lease or stops a server. Cross-file point-in-time consistency is not claimed.

The ZIP preserves associated world/dimension/player paths and ordinary hidden mod saves, `.yml` and `.properties` data. It omits only transient `session.lock`, reserved `.manager`, exact `manager.db` sidecars, `server.properties`, `.env` and `.env.*`, with path/reason disclosure in API/UI/manifest. `.key`/`.pem` candidates refuse capture. Manifest hashes cover exported files. Validation checks the exact expected file and directory inventory, zero-length directory entries, regular file modes, safe names and SHA256 before HTTP success. Audit records the prepared archive's hash/bytes, without source contents or credentials.

Root-reviewed bounds: 100000 entries, 256 dimensions, depth32, component255 bytes, relative path4096 bytes, aggregate relative paths8MiB, per-file8GiB, source16GiB, archive16GiB+32MiB, discovery15seconds, capture10minutes and transfer10minutes. Two global exports retain admission until Archive.Close; failed/canceled capture and completed/failed HTTP transfers clean their staging files.

Startup ReconcileExports requires the manager lock and paused queue dispatch, holds global world-export admission exclusion and refuses any active capture/transfer across service objects. It visits only persisted instance IDs, opens backups/.exports through rooted directory identity checks and removes only exact `[0-9a-f]{32}.zip` regular single-link staging files after opened/current identity checks. Partial ZIPs are eligible because a crash may interrupt creation. Unknown names/directories, symlinks, hard links and special files are preserved. Cleanup uses the discovery deadline and a global entry budget, is repeatable, and reports errors. Durable queued work is preserved without taking WithIdle, which would otherwise reject queued jobs and prevent startup forever. Lifecycle composition calls it before HTTP admission.

Viewer/operator/admin can discover; only operator/admin can download. Existing GET/read personal token semantics apply subject to current owner role. Expired/revoked tokens and disabled owners are rejected. The owned API fragment has two GET operations and no upload/import endpoint. Console integration owns fragment merging and generated TypeScript regeneration.

The Mantine world page uses generated API types, shows absent/ambiguous states, dimensions and omissions, wraps long paths, gates the attachment action by role, and exposes loading/error/status feedback plus the live-copy caveat. Downloads use browser-managed attachment navigation through a named same-origin frame; multi-gigabyte archives are not buffered into JavaScript Blobs. A failed JSON capture response is surfaced in the page. Native attachment/error behavior still needs integrated browser verification.

## Evidence from this resumed worker

Ordinary owned touch/remove succeeded without sandbox_permissions after the permission transition. Existing implementation was retained.

- RED: ZIP tampering test accepted unexpected/traversal/duplicate/missing directories and symlink-marked files before the validation patch. The test now rejects all those cases, duplicate files and changed content while accepting the reviewed inventory.
- RED: recovery tests initially failed to compile because ReconcileExports did not exist. Tests now exercise orphan deletion/idempotence, unknown instance/name/directory preservation, link/target preservation, active transfer refusal, new-capture exclusion across services, canceled/bounded cleanup and linked staging-root refusal.
- RED: integrator review identified that per-instance WithIdle blocked startup for persisted queued jobs. A real paused jobs queue and durable queued job reproduced `instance_busy`; cleanup now succeeds after an active Archive closes, removes the orphan and leaves the job queued. Startup-only cleanup retains global capture/transfer exclusion.
- `go test -race ./internal/world -count=1`: PASS after the startup admission fix, 4.864 seconds.
- `go test -race ./internal/api -run '^TestWorldHTTP' -count=1`: PASS. Exercises real instance/SQLite/queue/auth service routing, anonymous/all-role access, read/operate/admin token scopes, revoked/expired/disabled users, ZIP headers/body, queue reservation release before transfer, busy reservation refusal and unsafe capture failure before success.
- `go vet ./internal/world`: PASS.
- `GOOS=windows GOARCH=amd64 go test -c ./internal/world -o /home/thim/.cache/dilla-tmp/minecraft-world-task12.test.exe`: PASS. Cross-compilation only; no native Windows execution claim.
- `go test ./...`: PASS across the shared working tree after main composition appeared; some existing package results used Go's cache. No observed failures were omitted.
- `npm run typecheck` and `npm run lint` in web: PASS after console2 merged the API fragments and regenerated schema.d.ts.

Package fixtures use real temporary directories and malformed names/links, but synthetic file contents. Coverage includes 1.12.2 legacy,1.20.5 custom paths,1.21.9 Paper split roots,26.1 standard/player folders, bounds/cancellation/audit failure, reserved/unsafe paths and names with spaces, source replacement/change, archive completeness, digest checks and global transfer admission.

## Primary layout evidence reviewed

These publisher sources were read on2026-10-16. Their documented layouts informed discovery; they are not executed game qualification.

- [Mojang26.1 release notes](https://www.minecraft.net/en-us/article/minecraft-java-edition-26-1) document standard dimension data under `dimensions/minecraft/` and the `players/` directory reorganization.
- [Paper26.1 announcement](https://papermc.io/news/26-1/) shows the modern shared world structure, Paper dimension data and player directories.
- [Paper migration reference](https://docs.papermc.io/paper/migration/) documents legacy Paper split roots, the pre26.1 Vanilla/Paper comparison and Fabric/Forge's use of Vanilla structure.
- [Forge Saved Data reference](https://docs.minecraftforge.net/en/latest/datastorage/saveddata/) documents dimension-specific mod persistence and shared Overworld data.

## Remaining qualification

Root owns integrated390px/mobile keyboard/visible feedback review and a native browser attachment/error check after composition. No actual Minecraft process, EULA acceptance, real client connection, service install or production-host change occurred in this package. Real saved-world coverage and native Windows junction/reparse/filesystem execution remain required for release claims. Other release-matrix codecs and map decoding belong to their respective packages. World downloads remain a best-effort export and do not substitute for Task13 online backups.

## Independent review fix round 1 — base b4f27bf

The failed-capture cleanup defer discarded Archive.Close errors. It now joins the original capture failure with a wrapped cleanup error before clearing the archive reference. Archive.Close also joins file-close, staged-remove and root-close errors so each failed cleanup step remains inspectable with errors.Is/errors.As. Cleanup still makes one bounded removal attempt and releases transfer admission. A failed removal can leave a staged file requiring the existing startup reconciliation or operator repair; that failure is now returned to the service caller. No automatic removal of unknown paths or retry loop was introduced.

World warning strings are deduplicated at render time, preserving first occurrence order and giving each rendered warning a unique text key.

Focused regression: `internal/world/archive_test.go:TestCaptureReportsOriginalAndCleanupFailures` injects an original source-refresh failure after staging, moves the real open staged file into a nonempty directory at its original name and thereby causes rooted removal to fail. It verifies no archive is returned, the original sentinel survives errors.Is, the failed staging path survives errors.As and the staged bytes actually remain. The test uses real filesystem operations rather than a production injection hook.

- RED: `go test ./internal/world -run '^TestCaptureReportsOriginalAndCleanupFailures$' -count=1` failed with `staged removal failure lost injected source refresh failure` before the patch.
- The first post-patch assertion assumed the platform operation name `remove`; rooted Linux removal reports `removeat`. The assertion was corrected to inspect the actual staging path, retaining the failure-cause assertion without depending on the operation spelling.
- GREEN: `go test -race ./internal/world -run '^(TestCaptureReportsOriginalAndCleanupFailures|TestCaptureRejectsBoundsCancelAndUnprivilegedWithoutArchive|TestCaptureGlobalAdmissionEndsOnClose)$' -count=1` passed: `ok github.com/jonasthim/minecraft-server-ui/internal/world 2.472s`.
- `go vet ./internal/world`, `git diff --check` and web `npm run typecheck`: PASS.

No broad test suites, staging or commits were run for this fix round. Native Windows cleanup failure execution and browser warning rendering remain outside this focused evidence.

## World source-root follow-up — base 618c4fa

The initial absolute Lstat/OpenRoot pair checked only the final instance directory. An ancestor substituted after Paths.For could redirect both observations to the same outside directory. The private openWorldRoot bootstrap now opens only the filesystem root or a local Windows drive root, then descends each component through openChild's rooted, link-rejecting identity checks. It closes each ancestor after obtaining the checked child and closes the current root on errors, preserving close errors alongside the original failure. Noncanonical/relative paths and unreviewed UNC/device/extended-volume roots are rejected. openChild now preserves host stat/recheck errors instead of converting them all to a changed-world error.

Directory safety checks reject symlinks on every platform. Windows additionally checks FileInfo's FILE_ATTRIBUTE_REPARSE_POINT attribute on before/opened/current directory observations, covering junction/reparse candidates without relying solely on ModeSymlink. Native Windows execution is still unqualified.

The deterministic regression in internal/world/root_test.go completes Paths.For, replaces either the configured data directory or its instances ancestor with an outside-directory symlink, and calls the same private bootstrap used by openInstance. The old final-only bootstrap read `outside secret world` in both cases. The repaired bootstrap returns no usable root; discovery and capture also refuse the substituted paths. Other tests retain ordinary world reads, exercise closed/missing-host errors, and reject nondirectory, relative and noncanonical roots. No production injection hook, shared helper or constructor/interface change was added.

The scoped race run also reproduced an existing test-only synchronization flaw: TestReconciliationExcludesNewCaptureAcrossServices could time out because its TryLock polling briefly occupied admission when ReconcileExports tried its own nonblocking lock. The test now waits for the real SQLite connection's WaitCount to increase while its deliberately held connection blocks the reconciliation query. This observes admission without competing for it. Production admission and cleanup behavior are unchanged.

- RED: `go test ./internal/world -run '^TestWorldRoot' -count=1` failed both `data` and `instances` substitution cases with `ancestor substitution admitted outside world: "outside secret world" <nil>` against the extracted, unchanged old bootstrap.
- GREEN: the same focused command passed after anchored descent, `ok .../internal/world 0.015s`.
- An initial `go test -race ./internal/world -count=1` reported the existing synchronization failure: `TestReconciliationExcludesNewCaptureAcrossServices: reconciliation never acquired admission context deadline exceeded`. A later run before the test correction passed; the race was in test observation, not the production root fix.
- Final `go test -race ./internal/world -count=1`: PASS, `ok .../internal/world 6.544s`.
- `go test -race ./internal/world -run '^(TestWorldRoot|TestReconciliationExcludesNewCaptureAcrossServices)' -count=5`: PASS, `ok .../internal/world 4.714s`, checking the corrected synchronization and root regression repeatedly.
- `go vet ./internal/world`, `git diff --check`: PASS.
- `GOOS=windows GOARCH=amd64 go test -c ./internal/world -o /home/thim/.cache/dilla-tmp/minecraft-world-root.test.exe`: PASS; compilation only, not native Windows reparse or drive qualification.

Only internal/world files and this report were changed for this follow-up. Existing archive validation, cleanup and UI behavior were preserved. No game/EULA/service action, dependency, shared file change, staging or commit was performed. The patch is frozen for independent scoped review.

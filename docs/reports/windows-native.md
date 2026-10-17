# Native Windows CI portability repairs

2026-10-17. Source evidence: GitHub Actions run `39022162348`, application revision
`98540a2`, native Windows Server 2022 x64 with Go 1.27 and the race detector.
The sanitized local transcript is
`/home/thim/.cache/dilla-tmp/minecraft-ui-ci-windows-second.log`.
The repairs below await independent integration review and a new native run.
They do not qualify a stable release, installed services, a real JVM/client,
Windows 10/11, power-loss recovery, or the complete release matrix.

## Confirmed causes and repairs

1. **SQLite drive names became URI authorities.** `net/url` rendered a path
   such as `C:/Users/.../manager.db` as `file://C:/Users/.../manager.db`, producing
   `invalid uri authority: C:`. The same construction existed in `db.Open`,
   `ReadOnlySchema`, and `ReadOnlyActorAuthorized`. Shared `db.FileURI` inserts
   the path-leading slash for drive paths and retains URL escaping. It encodes
   already validated absolute paths; it does not authorize or expand paths.
   Normal opening retains absolute-path resolution, directory creation and
   WAL/FULL/foreign-key/busy-timeout verification on every physical connection.
   Probe opening retains `mode=ro` and `query_only`, without migrations.

2. **Windows executable identity compared different filesystem spellings.**
   Capture normalizes the configured executable with `filepath.EvalSymlinks`,
   while `QueryFullProcessImageName` can return the launch spelling containing
   8.3 aliases such as `RUNNER~1`. Windows comparison now resolves both existing
   complete paths with `EvalSymlinks` before its existing case-insensitive
   comparison. Go's Windows implementation walks the path and uses
   `FindFirstFile` to recover each component's filesystem spelling. Boot GUID,
   creation time, PID, complete image path and absolute launch-directory
   requirements remain enforced. A path lookup failure is a metadata mismatch;
   it is never reclassified as conclusive process exit. Windows still binds cwd
   through the private authenticated launch record because no documented remote
   cwd API is used. No basename, hard-link identity or checksum equivalence was
   substituted for the complete configured path.

3. **Executable copy flushed a read-only handle.** After hashing, copying and
   flushing bytes, `copyVerified` reopened the destination with `Open`, changed
   its executable permissions, and called `Sync`. Windows `FlushFileBuffers`
   requires write access. Reopening with `O_RDWR` preserves permission/flush/
   close error propagation. Hash validation, exclusive destination creation,
   source checks, journal ordering, privileged volume fences, rollback policy
   and world-data exclusions remain in place. No flush error is ignored.

4. **One service fixture supplied the wrong platform artifact.** The service
   discovers stages for `runtime.GOOS`, but its reload/retry test always staged
   a Linux archive. The fixture now selects the host platform, executable name,
   archive and architecture. Production platform verification is unchanged.

The launcher fixture also checks its completion channel while waiting for
control metadata, so an early failure reports its real error immediately.
The seven baseline missing-control errors are consistent with early launcher
identity rejection; a native rerun must confirm that diagnosis or expose the
next error through the improved fixture.

## Complete baseline failure inventory for the owned packages

Names below are all failed top-level tests in the four assigned packages;
failed subtests are listed where they narrow the cause.

| Package | Failed tests | Baseline cause |
| --- | --- | --- |
| `internal/db` | `TestOpenMigratesAndPersists`; `TestOnDiskDurabilityAcrossConnectionReplacementAndReopen`; `TestDiskConnectorRefusesIncorrectActualSettings` (`journal_mode`, `synchronous`, `foreign_keys`, `busy_timeout`) | Drive URI failure |
| `internal/launcher` | `TestIdentityRejectsReuseAndMissingBoot`; `TestAcceptedStopClassifiesExitBeforeQueueConsumption` | Image spelling mismatch |
| `internal/launcher` | `TestLauncherRealProcessControlLeaseAndWait`; `TestLauncherEOFGracefulStopWaitsForJVM`; `TestLauncherTimeoutReportsForcedStop`; `TestControlRejectsUnauthenticatedRequests`; `TestAdmissionBlocksConcurrentLauncher`; `TestOperatorStopRequiresDurableIntent`; `TestEOFReportsRecoveryFailureAfterCleanJVMExit` | Control metadata absent; early exit error was hidden |
| `internal/supervisor` | `TestAlreadyDeadStopWaitsForLauncherCompletion`; `TestAlreadyDeadStopPreservesFinalFailure` (`forced`, `recovery`, `missing`, `crashed`, `clean`) | Image spelling mismatch in fixture identity capture |
| `internal/selfupdate` | `TestDrainReceiptRequiresCleanDBAndClosedDBPublication`; `TestDrainRefusesInstanceUpdateAndWorldRiskTransactionsAndOrphanMarkers`; `TestDrainRequiresValidatedTerminalSupersession`; `TestSignedUnchangedSchemaCandidateWithHigherEmbeddedSchemaLeavesDBAndBinaryUntouched` | Drive URI failure; last test fails in read-only probe |
| `internal/selfupdate` | `TestNativeActivationAndHealthFailureRestoreOnlyExecutable` (`healthy`, `failed_health`); `TestNativeInterruptedReplacementRecoversOldExecutableAndRefusesSecondRollback` | Previous-executable flush access denied |
| `internal/selfupdate` | `TestCanceledActivationRestoresAndRestartsPrior` (`health`); `TestActivationDeadlineCleanupRestoresPriorExactlyOnce` (`health`) | Activation aborts before candidate start, consistent with the same copy/flush failure |
| `internal/selfupdate` | `TestStageStatusAndRetryRecoverPublishedReceiptWithoutNetworkOrActivation` | Linux-only fixture on Windows |

## Regression coverage and local evidence

- Observed the new drive URI regression fail with `file://C:/...` before the
  slash repair, then pass. It checks drive syntax and reserved URI characters
  without executing a Windows path on Linux.
- Real SQLite reopen/persistence coverage now uses a directory containing
  spaces, `#`, `&` and `%`. Disk settings and connection replacement checks
  remain active.
- New schema/actor probe test checks a real database in a special-character
  directory, preserves its exact bytes and existing schema, checks admin,
  viewer, disabled and missing users, and proves a missing database is not
  created.
- New native Windows image test compares actual process metadata, captures
  identity through the available short spelling, verifies the live identity,
  and rejects a different existing file and an unavailable image binding.
  If the volume supplies no distinct short alias, the test records that limit;
  it still exercises the native process identity. No test is skipped.
- New copy regression checks verified copy success, source preservation and
  wrong-hash rejection using real filesystem roots. Existing native activation,
  rollback, cancellation, deadline and injected fence tests remain active.
- `go test -race ./internal/db ./internal/launcher ./internal/supervisor ./internal/selfupdate -count=1`: PASS on Linux.
- `go vet ./internal/db ./internal/launcher ./internal/supervisor ./internal/selfupdate`: PASS.
- `GOOS=windows GOARCH=amd64 go test -exec /bin/true ./internal/db ./internal/launcher ./internal/supervisor ./internal/selfupdate -run '^$'`: compilation PASS; test execution deliberately disabled. This supplies no native execution evidence.
- `git diff --check` for the owned packages: PASS. The integrator owns the full
  repository test run and hosted native rerun.

## Primary references

- [SQLite URI filenames](https://www.sqlite.org/uri.html): authority restrictions
  and `/C:/...` Windows drive interpretation.
- [Microsoft QueryFullProcessImageNameW](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-queryfullprocessimagenamew): documented Win32 image-path format.
- [Microsoft GetLongPathNameW](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-getlongpathnamew): short/long spellings and filesystem-query failure conditions.
- [Microsoft FlushFileBuffers](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-flushfilebuffers): required writable file handle.
- Go 1.27 source, `src/path/filepath/symlink_windows.go`: `evalSymlinks`,
  `toNorm`, and `normBase`, inspected locally to verify filesystem normalization.

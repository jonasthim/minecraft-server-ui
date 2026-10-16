# Task13 — online save lease and recovery

Implementation submitted for independent review; the integrator owns ledger acceptance. Task14 archive/restore details are in `task14.md`. Frozen consumer/composition contract: `.superpowers/sdd/2026-10-16-minecraft-server-ui/task13-14-contract.md`.

## Implemented boundary

- Original current-job authority and fresh exact JVM identity; durable lease journal stores random owner lease ID, authoritative job ID, full private process identity, original absolute expiry and recovery state before attempting save-off.
- Native `ArmSaveLease` atomically arms the independent launcher watchdog and delivers save-off. An acknowledged fresh console/MSMP flush is required before source capture.
- Native lease is capped at 4m58s, never renewed. Copy/flush context ends thirty seconds before expiry to reserve bounded saving cleanup. Five minutes remains the hard policy ceiling. A stalled OS/JVM cannot be promised physical delivery/ack by a specific wall-clock instant; watchdog scheduling/delivery failures remain explicit.
- Copy/failure/cancellation/panic paths attempt saving recovery with independent thirty-second cleanup context while original console callback/admission is retained. Failed arm is also followed by saving recovery. Save-on acknowledgement is required before compression, archive validation, publication or ready state.
- `Session.ReleaseSavingForBackup` retains original cleanup authority, attempts known lease release and falls back to literal save-on when absent/expired/error. Any lease uncertainty prevents ready publication.
- `RecoverSaving` is restricted to paused startup admission, exact identity and console serialization. It bypasses only an existing watchdog error for saving repair; no arbitrary command/session bypass exists. Fresh cursor precedes command delivery; unavailable cursor still does not skip attempted delivery. Fresh save-on acknowledgement with uncertain old lease clearance only closes after the original expiry.
- Startup inspects old identity before classifying missing/dead/replaced JVMs; no recycled PID receives commands. All abandoned leases are attempted before later startup recovery can fail. Saving repair failure leaves queue paused.
- Stopped backups hold original instance stopped-session/native admission while source data is staged. Unknown process state never falls through to a stopped copy. `CheckReady` blocks ordinary mutations while saving/restore recovery is pending.

## Verification

The following were run successfully during implementation:

```
go test ./...
go vet ./internal/backup ./internal/instance ./internal/console ./internal/launcher ./internal/api ./cmd/minecraft-ui
go test -race ./internal/backup ./internal/instance ./internal/console ./internal/launcher ./internal/api ./cmd/minecraft-ui
GOOS=windows GOARCH=amd64 go test ./internal/backup ./internal/instance ./internal/console ./internal/launcher ./cmd/minecraft-ui -run '^$' -exec /bin/true
```

Targeted evidence includes `TestLeaseAlwaysReleasesBeforeReturning`, `TestLeaseDoesNotArmWithoutDurableJournal`, `TestLeasePanicStillAttemptsSaveOn`, `TestLeaseRejectsExtendedDeadline`, `TestLiveBackupLeaseReleasePrecedesArchiveAndSecretsStayPrivate`, `TestBackupFailuresNeverPublishAndAlwaysRelease`, `TestLeaseRecoveryWaitsForOriginalExpiryWhenClearanceUncertain`, `TestSavingRecoveryRequiresPausedAdmissionAndFreshExactIdentity`, and `TestSavingRecoveryAttemptsDeliveryWithoutCursor`.

Failures are injected at arm/flush/copy/release/cancellation, compression, validation, publication and SQL commit. Real filesystem capture and SQLite are used; the console/JVM fixture is explicitly substituted. Existing launcher tests `TestLauncherRealProcessControlLeaseAndWait`, `TestLeaseExpiresAttemptsSaveOnAndRejectsExtension` and `TestLeaseRetriesFailedSaveOn` remain passing.

Main composition tests prove native prior-process handling precedes lease repair, lease repair precedes restore/config/lifecycle recovery, retained jobs stay queued until successful recovery, failure prevents later Windows autostart/dispatch, and ordinary stopped readiness gates do not recursively block paused recovery.

## Qualification limits

No Minecraft server was installed, launched, stopped, or EULA accepted in this task. No service was installed. Native Windows execution and real game/version/platform backup/watchdog qualification remain unestablished. A Windows cross-compile is recorded only as compilation evidence. Live player data and local addon databases are explicitly imperfect even with native save flushing; external databases are excluded.

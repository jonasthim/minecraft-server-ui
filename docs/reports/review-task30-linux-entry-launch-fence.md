# Task30 Linux entry, launch, and updater-fence review

## Findings

### Medium: dispatch failure rewrites a published terminal result

`internal/install/entry_linux.go:281-291` reads and validates a worker result after `systemd-run` reports an error, but then changes `OutcomeInstalled` and `OutcomeUninstalled` to `OutcomePending` (`285-286`). The frozen entry contract requires returning a valid published result truthfully, adding the fixed `FailedStage="worker"` when needed, and still returning an error for the failed dispatch. Rewriting the outcome discards evidence that the worker completed and makes the result inconsistent with its durable result file. Preserve the outcome and attach the worker failure stage; add a test for both installed and uninstalled results on dispatch error.

### Medium: canonical updater lock path is not revalidated after `/proc/locks` read

`internal/install/launch_linux.go:820-835` obtains `manager.lock` metadata, then reads `/proc/locks` and accepts the row matching that captured device/inode. It does not revalidate that `stateRoot/manager.lock` still names that inode after the read. If the lock path is replaced during that I/O, the proof can match a live flock on the old inode while a subsequent updater opens the replacement inode and acquires a different lock. Recheck the held state root and canonical lock identity after reading `/proc/locks` (or pin and compare the opened lock file), and add a private-fixture replacement test.

## Reviewed boundaries

- `cmd/minecraft-ui-update/main.go:29-40` handles installer commands first and calls `CheckInstallerStartup` only for the exact `recover` argv, before `RunNativeUpdater`. The root-owned `minecraft-ui-update-recovery.service` is `Required`/`After` for the manager service and runs this recovery command before manager startup. This closes the startup authority concern for the planned integration: the check must remain on this root recovery path, not be moved into the unprivileged manager process.
- `install recover` dispatches `recover-worker`; the worker routes that command through `recoverWith`, while ordinary `worker` is restricted to `phasePrepared` and uses `continuePreparedWith` (`entry_linux.go:225-259, 354-365`).
- The ordinary updater fence checks the four fixed installer transaction/permit names and refuses both present and uninspectable entries. Ordinary Linux recovery fences before lock acquisition and again after acquiring the actual manager lock (`internal/selfupdate/launch_linux.go:303-320`); native updater status/request/run paths are fenced as well (`internal/selfupdate/native_linux.go:492-493, 525-526, 579-580`). I found no cleanup or interpretation of installer records in the ordinary updater.
- Launch permit checks bind the transaction, intent, image, executor, PID start token, executable inode, and actual kernel flock owner. Grant and startup paths repeat the lock-owner check after process/manager validation. The canonical-lock-path revalidation gap above remains.

## Verification and limits

- `go test ./internal/selfupdate -run 'TestInstallerFence|TestOrdinaryLinuxRecovery' -count=1` passed.
- `go test ./internal/install -count=1` could not compile the shared package in the current concurrent workspace: `internal/install/model_linux.go` has syntax errors beginning around line 253. This is outside this review's owned source and prevents running the entry/launch tests in the assembled package at this point.
- No systemd unit was started, no host account/service/game state was changed, and no native Ubuntu qualification was performed. The startup boundary review is based on source wiring and unit definitions.

## Integrator follow-up

Both reported findings are closed in source. Dispatch errors preserve a validated terminal result, add the worker failure stage when needed, and still return an error. Recovery dispatch has explicit controller and fixed-argv regressions for `recover-worker`.

Launch lock validation now retains the opened canonical lock and root, checks owner/mode/link metadata and exact identities before and after reading `/proc/locks`, and matches the kernel record to the pinned inode. New fixtures replace either the canonical lock or its state-root path during that read; both are refused.

Integrator combined race tests for the two CLI packages, installer and selfupdate passed after these changes; vet and Windows CLI cross-build also passed. The root recovery-unit gate routing is covered by command tests. **Approve these entry, launch and fence changes for integration.** This does not accept the still-evolving native driver or establish native Ubuntu lifecycle qualification.

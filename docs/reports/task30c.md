# Task30C — offline local administrator recovery

Status: implementation submitted for integrator review; package acceptance and native recovery qualification pending. This is the account-recovery subset of Task30, not the remaining installer/bootstrap/lifecycle work.

## Owned files and frozen interface

- `internal/recovery/recovery.go`, `internal/recovery/recovery_test.go`: stopped-manager recovery and isolated current-schema fixture tests.
- `cmd/minecraft-ui/recovery.go`, `cmd/minecraft-ui/recovery_test.go`: argument/config boundary and command tests.
- `docs/ADMIN-RECOVERY.md`: Linux/Windows stop, consistent DB/WAL backup, private stdin reset, start and login procedure.
- `docs/reports/task30c.md`: this report.

Frozen integrator dispatch target:

```go
func runAdminReset(ctx context.Context, args []string, input io.Reader, output io.Writer) error
```

Shipping syntax: `minecraft-ui admin-reset --username NAME --password-stdin`. `--username=NAME` and either flag order are accepted; unknown/duplicate/missing flags and positionals are refused without echoing values. No password argument, environment variable, generated password, HTTP endpoint, or setup reopening.

The integrator owns dispatch and the requested shared interfaces: `auth.NormalizeUsername`, `auth.ValidatePassword`, and `db.OpenExisting`. The latter preflights current schema read-only and opens existing SQLite through the durable connector with no mkdir, database creation, chmod or migration. Review those shared changes together with this subset. No other worker-owned files were edited by this worker.

## Behavior and safety evidence

Recovery checks an existing absolute private directory, all parent components, the regular non-symlink database and optional WAL/SHM/rollback-journal files, then acquires the real `launcher.AcquireManager` lock. It rechecks paths after acquiring the lock. The lock covers opening SQLite through database close, with the existing same-operating-system-account trust boundary.

The transaction reads authentication policy and the exact normalized target. Global disabled local login is refused; missing, disabled, viewer/operator, passwordless and OIDC-linked accounts are refused before reading the replacement secret. No account, role or authentication policy is created/changed by recovery.

Stdin is bounded to 131 bytes (128 password bytes + CRLF + overflow detection), accepts optional LF/CRLF and EOF, and rejects extra lines/trailing data, empty or invalid-length passwords. Existing `auth.HashPassword` creates the replacement. One transaction replaces the hash, deletes all target sessions and personal tokens, and appends `auth.admin_reset_offline` with an explicit `local-offline` actor, numeric user target and fixed redacted details. SQLite/Reader error strings are suppressed because they can include stored/input secrets. Context cancellation identity is preserved. No password, hash, session or personal-token value enters output or audit.

The fixture tests create accounts through the shipping auth service's Setup/Create/Login and token methods where practical, then exercise real current-schema disk databases. Tests verify new login succeeds and old password fails, exact target credential revocation, other users untouched, identity/role preservation, redacted offline audit, and rollback when a real SQLite trigger aborts each of the four write steps. Trigger errors contain a secret-like fixture string, proving those details do not escape.

Other tests cover each account refusal, global local-login policy preservation, stdin bounds/termination/error redaction and actual consumed-byte bounds, missing DB/directory and no implicit creation/migration, older/future/empty schemas, unsafe directory/DB/sidecar symlinks, cancellation before work and during input, real manager lock contention before input, lock ownership throughout password input, and subsequent release. Command tests cover unsafe parsing, configured data directory, failure output and successful revocation confirmation. Application settings and a queued job remain intact on policy refusal.

## Commands run (2026-10-17)

- Red check: `go test ./internal/recovery -run TestResetLocalAdminAtomicSuccess -count=1` failed against the placeholder with `offline recovery not implemented` before implementation.
- Policy red check: `go test ./internal/recovery -run TestResetRefusesDisabledLocalLoginAndPreservesApplicationData -count=1` failed before the policy check with `accepted recovery with global local login disabled`.
- `go test ./internal/recovery ./cmd/minecraft-ui -run 'TestReset|TestAdminReset' -count=1` passed after the shared interfaces landed (subsequent expanded coverage was included in the race run).
- `go test -race ./internal/recovery ./cmd/minecraft-ui -count=1` passed: final run after close-error handling and consumed-byte bound coverage: recovery 22.708s, command package 7.112s.
- `go vet ./internal/recovery ./cmd/minecraft-ui` passed.

Only disposable fixture state was reset. No real accounts, services, user data, game processes, EULA acceptance or preview actions were used. No dependencies added, staging or commits performed.

## Remaining evidence and limits

- Independent integration review must include dispatch, shared validators and `db.OpenExisting`, then update the ledger if accepted.
- Run the shipping command on disposable installed Linux and Windows runners. Existing native manager smoke results are not recovery evidence; neither cross-compilation nor Go fixture tests qualify native recovery or the documented shell backup commands.
- A generic `io.Reader` cannot be interrupted by context alone: cancellation is checked before/after the bounded read and before mutation; an unclosed blocked stdin still waits for EOF. The private-pipe examples write one line and close stdin.
- Path checks preserve the launcher's existing same-account trust boundary rather than defending against an adversary already controlling that account. Do not delete `manager.lock` or run deployment automation during the backup/reset procedure.
- A commit error reports uncertainty and asks the operator to verify state before retrying. A database-close or confirmation-writer failure after a successful transaction explicitly reports that reset already committed. Close errors are sanitized and joined with any prior error while the manager lock remains held. No deterministic SQLite Close failure is induced; normal close/lock release is covered by the real lock-lifetime test.

No Task30 acceptance or native recovery support claim is made here.

## Native acceptance harness addition (execution pending)

The source command at `59b437c` received independent specification PASS and quality APPROVE. The following harness addition is submitted separately for review; it does not change product behavior or qualify native recovery until the hosted runs are dispatched and inspected by the integrator.

Owned harness files:

- `deploy/qualification/manager_api.py`: tightens the initial hosted-runner guard and calls recovery after the existing real setup/persistence/stop/start/restart assertions, before the final native stop.
- `deploy/qualification/admin_recovery.py`: orchestrates installed CLI, shipping API checks, private snapshot validation and cleanup; catches external exceptions into fixed stage identifiers.
- `deploy/qualification/admin_recovery_snapshot_linux.sh`: copies SQLite under the exact Linux `flock`, tests independent lock contention and compares SHA-256 hashes while locked.
- `deploy/qualification/admin_recovery_snapshot_windows.ps1`: copies SQLite under `FileStream.Lock(0,1)`, tests independent first-byte contention and compares SHA-256 hashes while locked.
- `deploy/qualification/admin_recovery_test.py`: six local helper tests using temporary SQLite/WAL files or captured subprocess boundaries; no installed services/accounts.

`qualify_recovery(...)` receives the existing in-memory administrator credentials, client/session/identity, and existing HTTP/native control helpers. The real empty-database setup remains the only administrator creation path. A read-scoped personal token and another local viewer are created through the shipping API using its session CSRF token. The helper first attempts the installed command while the real manager runs and requires a manager-lock refusal, unchanged manager PID, and working old password/session/token.

After native stop and a zero-manager-PID assertion, platform helpers copy every existing `manager.db`, WAL, SHM and rollback-journal file while holding the manager lock. They require matching source/copy hashes and refusal of another lock attempt, then emit only fixed booleans and a file count. Python checks the copied file set and runs SQLite `integrity_check` on the private snapshot with WAL enabled (`mode=ro`, without `immutable=1`), including the setup administrator's existence. Snapshot files live in a unique private `admin-recovery-*` directory inside the canonical data tree and are deleted in `finally`. Existing native harness cleanup deletes the entire fresh data tree if interrupted; no new lifecycle-script cleanup path is required. Credential-bearing snapshots, hashes, subprocess output, HTTP bodies, passwords, cookies and tokens are never copied to artifacts.

The exact installed `admin-reset --username native-smoke-admin --password-stdin` then runs with a fresh in-memory password supplied only through a subprocess pipe closed at EOF. Linux uses the actual manager service account via `sudo -n -u minecraft-ui`; Windows uses the already-authorized Administrator identity from its native harness. After restart, assertions require one new manager PID, matching administrator ID/role, new-password login, rejection of old password/session/personal token, an empty recovered token list, and the other user's unchanged password/session and persisted identity/role. Existing final-stop/no-orphan assertions remain in place.

Local checks run for this addition:

- `PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s deploy/qualification -p admin_recovery_test.py -v`: six tests passed. The fixture requires an uncheckpointed WAL to recover its schema/account row; removing the WAL causes the read-only verification to fail. Other cases cover private-copy cleanup on success/failure, stdin-only secret transport, suppression of accidental secret echo, and hosted-runner refusal before callbacks.
- Python compilation/import checks passed for the two helper modules and test module.
- `bash -n deploy/qualification/admin_recovery_snapshot_linux.sh` passed.
- PowerShell is unavailable in this local environment, so parsing/execution of the new Windows helper remains a native-runner requirement.

Expected hosted evidence is the existing installed-manager Linux Ubuntu 22.04/24.04 and Windows Server 2022 workflow, now including three additional scrubbed `PASS recovery ...` lines in its existing `api-assertions.txt`. Root owns workflow dispatch and review. No workflows or lifecycle scripts were changed by this worker.

Native assertions do not cover SSO/policy/role rejection combinations, transaction fault injection, cancellation or commit/close failures; source fixture tests cover those boundaries. They preserve manager account/role/user state rather than claiming game/world recovery. The native snapshot records the actual existing sidecar count; clean shutdown may already have checkpointed/deleted WAL/SHM. The local uncheckpointed-WAL fixture covers that separate copy-consumption case. No hosted execution or native recovery completion claim is made in this appendix.

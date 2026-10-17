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

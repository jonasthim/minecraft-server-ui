# Task32B — native updater failure diagnostics

Status: diagnostic source candidate, not a demonstrated Windows runtime fix.
Windows pipe tests have been cross-compiled but not executed locally. The next
native run must identify the actual failing operation before a behavior change.

## Actual evidence and unresolved cause

[Run39034369430](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39034369430)
at `574c505b9d6c7f71c06e746ea485ab61a2a80c01` passes the shipping Windows
installer/bootstrap, private ACL checks and manager-account staging after the
reviewed ACL correction. Its scrubbed identity report records updater PID7156
as SYSTEM. The updater reaches Running, then is Stopped by the first installed
HTTP status assertion; SCM reports Win32 exit1066 and service-specific exit1.
The manager is Running with PID5096. No activation occurred on Windows.

Recovery precedes the updater's Running notification. Its serving path can
subsequently fail while loading the account receipt, constructing pipe security,
creating the pipe, or connecting a client. Every error previously became the
same SCM code1. The retained artifacts do not identify which operation failed;
a connection transient, pipe-name collision, account error or descriptor error
must not be asserted as the observed cause.

Microsoft documents that nonblocking `ConnectNamedPipe` can return success when
an instance becomes available to clients, `ERROR_PIPE_LISTENING` without a
client, `ERROR_PIPE_CONNECTED` for a connected client, or `ERROR_NO_DATA` after
a client closes before the server disconnects. A successful availability
transition alone is not proof of a live connection. These distinctions motivate
native tests; they do not prove which result occurred in this hosted run.
[ConnectNamedPipe](https://learn.microsoft.com/en-us/windows/win32/api/namedpipeapi/nf-namedpipeapi-connectnamedpipe)

Microsoft also describes immediate nonblocking read/write behavior, including
empty reads and message writes that can succeed without transferring bytes.
The existing fixed message sizes, wait mode and time limits remain in this
diagnostic packet.
[Named pipe type, read, and wait modes](https://learn.microsoft.com/en-us/windows/win32/ipc/named-pipe-type-read-and-wait-modes)

## Windows diagnostic contract

`native_windows.go` retains the native cause internally and exposes only a
numeric SCM service-specific exit code: `0x4dSSWWWW`. `SS` is a fixed operation;
`WWWW` is the underlying Win32 error only when it fits16 bits, otherwise zero.
No error text, path, SID, request, credential or pipe payload enters this code.

| SS | Operation |
| --- | --- |
| 01 | Recovery returned an error |
| 02 | Recovery deadline/cancellation |
| 03 | Protected manager account receipt/SID loading |
| 04 | Pipe security descriptor construction |
| 05 | CreateNamedPipe |
| 06 | ConnectNamedPipe |
| ff | Unclassified internal error |

Examples: `0x4d050005` means pipe creation/access denied; `0x4d0600e8` means
pipe connection/ERROR_NO_DATA. SCM's Win32 exit remains1066 for service-specific
failures. Existing `scm-summary.json` already retains the numeric field, so no
extra privileged reporting command is needed.

Pipe creation and connection handling are extracted into the actual shipping
helpers used by `servePipe`. The original connection branches are retained,
including their current treatment of a nil `ConnectNamedPipe` result. This is
deliberate: the next native tests may expose that branch as premature success.
No transient error is newly ignored, no client is newly authorized, and no
pipe permissions/name/remote-client/first-instance/PID/image checks are relaxed.
Cancellation returned from the extracted wait still closes its handle and
stops normally; native errors continue to fail the service.

`native_pipe_windows_test.go` exercises the production helpers using unique
local test pipe names and a DACL limited to the current test identity. It
requires no service/account installation and never opens the production name.
Tests cover idle waiting/cancellation, a client already connected before accept,
actual fixed-command transfer, a client closed before accept, first-instance
collision, fixed SCM encoding, nested native causes and omission of private
error text or oversized non-Win32 status values. Transport tests do not claim
installed manager/updater authorization evidence. The idle test is a falsifiable
contract and could fail on the unchanged connection logic in the native run.

## Bounded harness evidence and definitive terminal handling

The same run's Ubuntu24.04 job times out after310 seconds during genuine initial
activation. Its updater unit exits1 and the manager remains Running; no scrubbed
activation journal phase was retained. That phase cannot be recovered after
cleanup. Ubuntu22.04's separately successful installed cases do not qualify the
failed Ubuntu24.04 or Windows paths.

An actual local regression reproduces a harness error: `wait_terminal` raises
for an unexpected terminal phase inside `wait_for`, whose transient retry loop
swallows RuntimeError. An `aborted` terminal therefore becomes a misleading
timeout. The phase assertion now runs after the bounded wait returns a terminal
journal. Partial JSON/read errors remain retryable; an observed unexpected
terminal fails immediately, without relaxing expected phase or data checks.

`updater_api.py` now writes:

- `initial-update-status.json` before the initial assertion: fixture version,
  bounded integer schema, strictly typed booleans and a fixed reason category.
  Unknown strings or values are classified rather than copied.
- `activation-state-summary.json` when `wait_terminal` exits, on success or
  failure: at most16KiB is read; only a fixed phase, valid stage hash,
  prior/candidate fixture version/hash/schema and strict rollback boolean are
  retained. Absent, malformed, oversized or unreadable state gets a fixed label.
  Nonce, actor, timestamps, arbitrary fields/error messages and raw JSON remain
  private. A reporting write failure fails the harness with a fixed message.

`linux-updater.sh` changes only its final report chmod allowlist so these two
root-created scrubbed JSON files are available to artifact upload. Existing
Windows JSON artifact collection already includes them. Journal snapshots are
retained on terminal waits; failures before any terminal wait still rely on the
initial status and existing native service diagnostics.

## Local verification and next gate

Executed on Linux, 2026-10-17:

- The new real-file unexpected-terminal regression first failed: expected
  `unexpected native updater terminal state`, received `native updater terminal
  state timed out`. It passes after the assertion moves outside transient retry.
- `python3 -m unittest discover -s deploy/qualification -p updater_api_test.py -v`:
  PASS,10 tests. Real temporary journals cover partial-JSON retry, expected and
  unexpected terminal behavior, timeout retention, oversized/malformed input,
  primitive typing and secret/unknown-field omission. Clocks/sleeps are controlled
  to avoid a310-second test; no network or installed operations run.
- `go test -race ./internal/selfupdate`: PASS (`10.595s`), Linux-applicable tests.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o
  /home/thim/.cache/dilla-tmp/selfupdate-windows-pipe.test.exe
  ./internal/selfupdate`: PASS, compilation only.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet ./internal/selfupdate`: PASS.
- Bash syntax, Python compilation and scoped `git diff --check`: PASS.

Native Windows execution of the pipe tests and the installed updater must
follow review. The exact Windows failing stage/Win32 code and Ubuntu24.04
terminal/last journal phase remain unknown until that run. No Linux updater
product logic, ACL predicate/tests, shared API/schema, service installation,
native host state or release qualification was changed by this packet.

# Task32B — Windows availability diagnostics

Status: diagnostic source prepared for review. The remaining Windows
availability failure is unproven; no permission, process identity, readiness,
pipe protocol or timeout correction is inferred from a running service.

## Actual boundary

[Installed updater run39036772847](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39036772847)
at `d0994d106c77421eb36983a79e4601aeb69d065d` records manager PID5872 and
updater PID3728 both Running, with updater Win32/service-specific exits0.
Initial authenticated manager status reports genuine `v0.0.1`, schema20 and
configured trust, but activation is false with `native_not_ready`. No Windows
activation is reached.

[Native CI39036771126](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39036771126)
passes the previously reviewed closed-client recovery and collision tests on
actual Windows. That is evidence for the preceding pipe correction, not proof
of manager-account pipe/SCM/process access or this new diagnostic source.

The availability path previously discarded every pipe/identity/readiness error
into a boolean. Possible failures include pipe access, SCM query, updater process
image verification, message mode/write/read, server trust/receipt checks and
pending state. The existing artifacts cannot select one of these causes.

## Frozen composition and fixed taxonomy

The integrator approved the optional, stateless method
`AvailabilityStatus(context.Context) (bool, string)`. Its Windows implementation
performs the same canonical-installation and trust prerequisites, followed by
one pipe status probe. Success always returns an empty reason. It stores no
last-error cache and adds no mutation. The integrator owns the shared
`service.go` adapter and its single-call/legacy fallback tests; this packet does
not edit shared service/native contracts.

Failure text is exactly:

```text
Native updater unavailable (Windows code=0x4eSSWWWW).
```

`SS` is a fixed stage byte, and `WWWW` is a fitting16-bit Win32 error or zero.
Typed failures retain their cause internally through `Unwrap`; formatting never
copies error text, paths, image names, PIDs, SIDs, requests or pipe payloads.
Unknown stages become the fixed internal stage. Oversized native statuses are
omitted rather than truncated. Open/read deadlines retain the last native code
alongside cancellation internally, preserving the original three-second bound.

| SS hex | Fixed stage | Check |
| --- | --- | --- |
| 01 | trust | Manager native trust loading |
| 02 | pipe_open | Open pipe, including bounded retry failure |
| 03 | pipe_server_pid | Obtain actual pipe server PID |
| 04 | scm_connect | Open SCM with CONNECT only |
| 05 | service_open | Open fixed service with QUERY_STATUS only |
| 06 | service_query | Query service status |
| 07 | service_state | Running state and nonzero PID |
| 08 | process_open | Open actual service process with QUERY_LIMITED_INFORMATION |
| 09 | process_image | Query process image |
| 0a | image_path | Compare fixed expected executable path |
| 0b | image_stat | Inspect actual image |
| 0c | expected_stat | Inspect expected image |
| 0d | image_binding | Compare file identity |
| 0e | server_binding | Bind pipe server PID to verified updater PID |
| 0f | pipe_mode | Set existing nonblocking message mode |
| 10 | pipe_write | Write fixed command |
| 11 | pipe_write_count | Require complete command write |
| 12 | pipe_read | Unexpected native read failure |
| 13 | pipe_read_wait | Bounded response wait |
| 14 | server_refused | Existing server refusal before command acceptance |
| 15 | unexpected_reply | Unrecognized/invalid status reply |
| 16 | server_trust | Updater native trust loading |
| 17 | server_state | Updater protected state root |
| 18 | server_installed | Updater installed receipt |
| 19 | server_pending | Updater pending-state check |
| 1a | legacy_disabled | Exact old disabled reply |
| 1b | legacy_busy | Exact old busy reply |
| 1c | internal | Unclassified internal error |

Only the four checks in the authorized `status` command return new fixed wire
diagnostics, shaped `unavailable:4eSSWWWW` for stages16–19 hex. The client accepts
only that exact lowercase eight-hex-digit format and those four stages. Other
stages, arbitrary text, extra data or another protocol response become the fixed
unexpected-reply category. `ready` remains the only successful status reply;
activation commands/replies retain their existing behavior.

Pre-command client PID/image failures retain the existing `refused` reply;
diagnostics do not read a command before its identity check. This fixed refusal
category identifies that boundary without claiming its hidden subcheck. No
request data is included in a readiness response.

Actual process-image and pipe-server validation are extracted into shipping
helpers so native tests can exercise their failure routing using the current
test process or an invalid handle. SCM access masks, process rights, expected
paths, file identity comparison, pipe PID binding, DACL, first-instance/remote
refusal, command authorization and existing cancellation/timeout behavior are
preserved. No diagnostic result authorizes activation.

## Scrubbed hosted evidence

`updater_api.py` retains the existing fixture version/schema and boolean fields.
For only the exact authored reason above and stages01–1c, it records:

```json
{
  "disabled_reason": "windows_native_unavailable",
  "windows_availability": {"stage": "process_open", "win32_code": 5}
}
```

That is a **format example**, not the observed runtime cause. Raw reason text
is not retained. Unknown codes/stages, another prefix, changed case, suffixes,
newlines and non-string values remain `unrecognized` without the diagnostic
object. No artifact permission, public API schema or HTTP role is changed.

## Verification and remaining gate

Executed locally on Linux,2026-10-17:

- The new authored-reason retention test first fails against the old sanitizer:
  `unrecognized` is returned instead of `windows_native_unavailable`.
- `python3 -B -m unittest discover -s deploy/qualification -p updater_api_test.py
  -v`: PASS,12 tests. Exact diagnostic retention and bounded allowlist rejection
  pass alongside existing CSRF, terminal, journal-bound and secret-omission cases.
- `go test -race ./internal/selfupdate`: PASS (`13.777s`), Linux-applicable
  package tests on the shared candidate, including the separately owned Linux
  diagnostics present in this workspace. This is not Windows execution.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o
  /home/thim/.cache/dilla-tmp/selfupdate-windows-availability.test.exe
  ./internal/selfupdate`: PASS, compilation only.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet ./internal/selfupdate`: PASS.
- Python3.10 syntax and scoped `git diff --check`: PASS.

`native_availability_windows_test.go` adds fixed-code/error privacy, nested
native-cause retention, error-over-ready precedence, strict reply whitelisting,
canonical/canceled probes, and actual read-only process/invalid-pipe validation.
Its Windows tests remain unexecuted locally. They do not create accounts or
services or open the production pipe. The next reviewed native run must execute
these tests and retain the authenticated initial status diagnostic before a
remaining runtime cause or correction is claimed. No local native/service,
account, firewall, preview or game operation was performed.

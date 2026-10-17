# Task 20: SMTP cancellation fixture follow-up

Date: 2026-10-17. Separate integrator assignment after the Task 18 rollback
implementation freeze. Ownership is limited to
`TestSMTPResponseReadCancellationClosesPeer` in
`internal/notify/smtp_limits_test.go` and this report. Production SMTP code was
not changed.

The complete Go suite twice failed the existing cancellation test at its expected
`timeout` assertion with `connection_failed`. The isolated test passed. The
fixture signalled cancellation immediately after its peer accepted TCP, before
the client's `DialContext` necessarily returned. Cancellation could therefore
correctly abort connection establishment instead of testing SMTP response reads.
`sendEmailTLS` maps that dial failure to `connection_failed`; `smtpExchange` maps
cancelled SMTP I/O to `timeout` and the production cancellation callback closes
the raw connection. The test had not synchronized the phase it claimed to test.

The peer now sends its `220` greeting and reads the exact bounded
`EHLO localhost` command before signalling cancellation. It withholds the EHLO
response, placing the client at the intended SMTP response phase. Setup and
goroutine cleanup have finite deadlines, and unexpected setup/client completion
is reported explicitly. The original assertions remain: the client must return
`timeout` promptly and the peer must observe closure. A peer deadline error no
longer masquerades as successful client closure.

Verification:

- Before the fix, two `go test ./... -count=1 -timeout=180s` runs failed this
  cancellation test with `connection_failed`; the isolated test passed.
- `go test ./internal/notify -run '^TestSMTPResponseReadCancellationClosesPeer$' -count=100 -timeout=60s`: PASS (0.015s).
- `go test ./internal/notify -count=25 -timeout=120s`: PASS (3.446s).
- `go test -race ./internal/notify -count=10 -timeout=120s`: PASS (30.409s).
- `go vet ./internal/notify` and `git diff --check`: PASS.

The integrator requested no further full Go suite until final integration; this
follow-up uses focused repeated/race notification checks only. No
release/platform qualification, staging or commit is claimed.

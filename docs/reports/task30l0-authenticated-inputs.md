# Task30L0 authenticated bootstrap inputs

Status: implementation and focused source checks complete; awaiting integrator
review and the native candidate workflow rerun with the revised checker. This
prerequisite does not implement installer transactions or complete Task30.

`Prepare` now retains the exact `bootstrap.json` and `bootstrap.sig` byte slices
that passed `candidate.VerifyBootstrap` in that call. It writes them as ordinary
private files through the existing destination creation, synchronization,
identity-check and cleanup path, before archive acquisition and before the
`completion.json` receipt. The receipt format, signed archive members and
manifest format are unchanged. No public trust key is copied and no prepared
input is executed.

The focused regression independently parses the caller-supplied public trust,
reauthenticates the retained metadata and signature, checks exact original bytes
and the receipt metadata digest, verifies Linux file modes, and asserts the
complete output name set. Cancellation proves both inputs exist before archive
acquisition and that failed preparation cleans them without a receipt. A
replacement probe verifies cleanup retains a substituted foreign input while
removing the other owned input.

`candidate.VerifyBootstrap` requires canonical JSON, including its existing
trailing newline, for both metadata and the signature envelope. The regression
preserves those exact authenticated bytes without changing verifier behavior.

Evidence:

- Previous-negative-control run: the new retention regression failed against
  the baseline at `bootstrap.json` with `no such file or directory`.
- `go test ./release/bootstrap -count=1` passed.
- `go test -race ./release/bootstrap -count=1` passed.
- `go vet ./release/bootstrap` passed.
- `GOOS=windows GOARCH=amd64 go test -c -o /tmp/minecraft-server-ui-bootstrap.test.exe ./release/bootstrap` passed (cross-compilation only).
- `git diff --check` passed.

Native Windows ACL execution and the native candidate workflow with the updated
root checker remain pending. These source checks do not establish installer
lifecycle or Task30 completion.

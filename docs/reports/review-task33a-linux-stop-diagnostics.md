# Review: Linux stop process diagnostics

## Result

No blocking findings remain in the reviewed Linux stage diagnostic changes.
The stage accessor returns only the finite authored names and keeps the
underlying error available through normal Go wrapping for internal handling.
The lifecycle log path emits the fixed stage message; the new stage labels do
not include the wrapped error, filesystem paths, or broker output.

The Linux inspector preserves the existing stop decision boundaries. It makes
one direct process identity read after broker invocation or broker identity
validation fails. A changed boot/start identity or an unwrapped missing-process
error confirms the recorded process ended. A same, malformed, permission
denied, or otherwise ambiguous result preserves the original broker or
identity-validation error and does not permit the process to be treated as
stopped. Cancellation prevents the recheck, and cancellation during the recheck
also preserves the original failure.

The earlier regression where a boot-identity `ENOENT` could be mistaken for a
missing JVM is corrected. Linux process-stat, executable-link, and working
directory missing-process errors remain unwrapped; boot-identity failures are
stage-wrapped and do not satisfy `IsMissingProcess`. Tests cover both cases.
The same boundary is tested for a missing broker executable: its staged
`broker_path` `ENOENT` does not prove process exit. A live direct recheck keeps
that original stage and error; only the separate direct recheck can establish
process absence.
Tests also verify exact broker path/invocation/decode/identity stages, original
error preservation, changed-start and real-child-exit confirmation, and the
negative live, malformed, permission, and canceled recheck cases.

## Verification

- `go test ./internal/launcher` — passed.
- `go test -race ./internal/launcher` — passed.
- `go vet ./internal/launcher` — passed.
- `git diff --check` on the reviewed launcher files — passed.

This source review does not identify the cause of any hosted stop failure and
does not qualify the Linux package on a hosted native runner.

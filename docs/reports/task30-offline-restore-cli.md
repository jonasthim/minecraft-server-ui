# Task30 offline restore CLI

The stopped-manager command now dispatches the approved explicit restore
workflow through `managerstate.Restore`. The accepted forms are:

```text
manager-state restore --snapshot HEX_ID --confirm-manager-state-restore
manager-state restore --snapshot HEX_ID --confirm-manager-state-restore --resume-version VERSION --resume-executable-sha256 HEX_SHA256
```

The second form requires both resume-image fields, in that order. Snapshot IDs
and executable hashes use the existing canonical lowercase 64-hex validation.
The library validates version syntax before it performs I/O. The command prints
the typed maintenance summary and returns the library's safe wrapped error,
preserving context cancellation identity. Invalid grammar is rejected before
configuration is loaded.

For a snapshot at the currently embedded schema, the library binds resume to the
current manager image. An older-schema restore requires the exact future resume
version and executable hash. Restore creates a verified safety snapshot and
leaves startup held for explicit record/world-state review. The existing
`resume --snapshot HEX_ID --confirm-manager-state-review` command performs that
review transition only when the matching image is running.

The command fixture exercises the real CLI dispatcher. It snapshots a latest
schema manager database, changes a selected instance row, restores the captured
row, checks that the safety snapshot retained the changed row, and proves a
world marker was not changed. It also checks the held startup state, explicit
resume, older-schema refusal without a binding, and acceptance of a complete
explicit future-image binding in an isolated temporary data directory.
Malformed, partial, reordered, duplicate, uppercase-hash, and extra restore
arguments are rejected without output.

Verification performed:

- `go test ./cmd/minecraft-ui -count=1` — pass.
- `go test -race ./cmd/minecraft-ui -count=1` — pass.
- `go vet ./cmd/minecraft-ui` — pass.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./cmd/minecraft-ui` — pass
  as a cross compile only.
- `git diff --check` on the owned files — pass.
- Native Windows execution and installed recovery qualification are not claimed.

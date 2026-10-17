# Task30L6 manager hooks

Implemented the local maintenance drain and readiness hooks in
`internal/selfupdate/maintenance_hooks.go`. The maintenance challenge has a
separate format and operation binding from the actor-bound web update
challenge. Both challenge readers reject the other challenge filename whenever
their own challenge file is also present, including when either file is
malformed. If their own file is absent, they return `os.ErrNotExist` so the
opposite prepare path remains a no-op.

Both drain paths now call the same private `verifyDrainedState` helper. It
retains the existing read-only migration, job, save-lease, restore,
transaction, supersession, instance-count, and per-instance marker checks.
Maintenance additionally binds the receipt to the current version, schema,
and executable hash. A fresh-install apply challenge can be parsed, but a
running manager refuses to fabricate a prior-image drain when `From` is
absent. No web actor lookup is performed for the local maintenance path.

Receipt publication creates the fixed pending file exclusively, revalidates
the challenge, then publishes with atomic `Root.Link` so an existing output
cannot be replaced. It verifies that both names refer to the owned regular
file, syncs the directory, removes only the identity-matched pending name, and
checks link counts of one before publication, two while both fixed names exist,
and one after removing the identity-matched pending name. It requires a final
absent-pending/output-identity fence and directory sync before returning
success. If publication becomes uncertain after linking, it returns an error
and preserves the output and pending name for recovery. Any later privileged
receipt consumer must refuse to consume a receipt while
`maintenance-drained.pending` exists.

The directory sync is a Unix durability fence. The Windows `syncRoot` hook is
intentionally a no-op because this manager hook runs without installer
elevation. On Windows, treat the receipt only as ephemeral evidence for the
current live operation. Before durable journal or asset changes, the privileged
consumer must revalidate the fresh challenge and receipt, confirm pending is
absent, verify manager exit and the data lock, and fence the volume. After a
reboot it must re-probe the stopped state instead of reusing an old receipt.

The readiness handler is bound to the apply challenge's target version, schema,
and executable hash. It accepts one loopback GET with the exact bearer nonce,
re-reads the challenge on request, and consumes the response only after the
manager readiness callback and executable hash check succeed.

## Verification

- `go test ./internal/selfupdate -count=1`
- `go test -race ./internal/selfupdate -count=1`
- `go vet ./internal/selfupdate`
- `GOOS=windows GOARCH=amd64 go test -c ./internal/selfupdate -o /tmp/selfupdate-windows.test.exe`
- `git diff --check` on all owned Go and report files

The publication tests cover an existing output, receipt/challenge mismatch,
pending-name absence after successful publication, an output collision created
immediately before the exclusive link, an outside hardlink, and cancellation
during pending-file write. They confirm foreign output and uncertain aliases
remain intact while owned pending cleanup is identity-bound. Challenge-reader
tests cover ordinary-only, maintenance-only, and simultaneous files through
both prepare paths. A table-driven cross-flow test exercises every SQL and
per-instance marker blocker through both ordinary and maintenance preparation.
Readiness tests cover loopback, method, nonce, one-use behavior, challenge
replacement, and retry after not-ready. These are package fixtures only;
startup/readiness wiring and the privileged receipt consumer remain integration
work.

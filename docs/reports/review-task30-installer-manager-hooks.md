# Task30L6 manager hooks review

## Result

Reviewed the frozen Task30L6 contract, manager hook implementation, package
tests, and composed `cmd/minecraft-ui` shutdown/readiness wiring. No remaining
material correctness or contract gaps were found in the reviewed scope.

The earlier challenge-composition issue is corrected: each flow first checks
its own fixed challenge file, returns `os.ErrNotExist` when absent, and checks
for the opposite challenge only when its own file exists. Both readers reject
simultaneous files before parsing. The composed tests exercise ordinary-only,
maintenance-only, and simultaneous challenge cases through both prepare
paths.

Both prepare paths now use the same read-only drain verifier. The table-driven
regression exercises all nine existing SQL blocker queries and each fixed
per-instance transaction marker through both flows. The local maintenance path
also binds source version, database schema, and executable hash to the
challenge's `From` image.

Publication writes the pending file exclusively, verifies its identity and
single-link count, and uses `Root.Link` for no-replace output creation. It
checks the two names refer to the same owned regular file, validates the
expected link count while both names exist, removes only the identity-matched
pending name, and verifies the final one-link output and pending-name absence.
Failures after publication preserve uncertain names for recovery. Tests cover
pre-existing output, a collision at the publication boundary, an external
hardlink, cancellation cleanup, and receipt/challenge mismatch.

The root composition prepares both applicable proofs while the database is
open, closes the database before publishing, skips preparation after an
earlier shutdown error, and publishes nothing if preparation or close fails.
The caller still holds the manager lock until the deferred close path returns;
the composed fixture verifies the database is closed and a second lock
acquisition remains refused when publication succeeds. Both readiness routes
share the same startup/health admission callback while retaining distinct
challenge protocols.

Windows durability is accurately bounded: manager-side directory sync remains
the existing no-op, and the receipt is documented as ephemeral evidence for
the current live operation. The later privileged adapter must validate a
fresh challenge/receipt, pending absence, process exit and lock ownership,
then fence the volume before durable or asset-changing actions. No Windows
durability claim is made by this packet.

## Verification

- `go test ./internal/selfupdate -count=1` — passed.
- `go test -race ./internal/selfupdate -count=1` — passed.
- `go vet ./internal/selfupdate` — passed.
- `GOOS=windows GOARCH=amd64 go test -c ./internal/selfupdate` — passed.
- `git diff --check` for the reviewed tracked implementation files — passed.

The worker additionally reports full selfupdate race/vet/Windows compile and
diff-check after the final cross-flow test update. The composed manager tests
were reviewed in source; root owns their combined race/vet gate. This review
does not establish startup wiring or privileged installer consumer acceptance
beyond the inspected current composition.

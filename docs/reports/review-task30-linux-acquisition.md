# Independent review: Task 30 Linux acquisition

Reviewed `internal/selfupdate/installer_linux.go` and
`internal/selfupdate/installer_linux_test.go` against the Task 30 Linux
acquisition brief and packet. This was a source review only; no host-side
operations or tests were run.

## Findings

### High: `OpenState` can return a root that was not compared with the session root

At `installer_linux.go:221-227`, the method opens a clone by the public fixed
path, then calls `verifyMaintenanceRootBinding` on the session root again. That
helper compares the *session root* with the current path, but never compares the
newly opened `clone` with `s.state`. If the path is swapped between the
precheck and clone open, then swapped back before the second binding check, the
second check succeeds and the method returns a root pinned to the replacement
directory. This violates the frozen interface's requirement that `OpenState`
return a pinned clone of the same protected root. Compare `clone.Stat(".")`
against `s.state.Stat(".")` (and retain the path revalidation) before returning.

### Medium: valid residual activation challenges can be rejected during terminal reconciliation

`readTerminalControl` bounds challenge expiry to `RequestedAt + 5 minutes` at
`installer_linux.go:561-570`. The producer creates the initial activation
challenge with `ExpiresAt: time.Now().UTC().Add(9*time.Minute)` at
`native.go:264`; request time may already be up to a minute old. `ClearControl`
can fail after a terminal journal is written, leaving that valid challenge for
installer reconciliation. Its expiry can therefore exceed the installer's
five-minute bound, so a normal `healthy` or `aborted` terminal record with a
leftover challenge is classified as foreign and cannot be cleaned. Align the
accepted interval with the producer's actual challenge lifetime and bind its
other fields to the terminal record as currently done.

### Low: the `aborted` journal phase accepts an impossible rollback flag

The terminal predicate at `installer_linux.go:313` requires
`RollbackAttempted` for `rolled_back` and forbids it for `healthy`, but accepts
`aborted` with either value. The producer's `aborted` transitions occur before
rollback and leave `RollbackAttempted` false (`native.go:280-285` and
`native.go:303-312`). Since the brief requires strict terminal journal
validation, an `aborted` record with `rollback_attempted: true` should be
refused rather than treated as cleanup-authorized terminal state.

## Initial acceptance-evidence gaps

These gaps were identified in the first review. The round 1 re-review below records the added tests and remaining challenge timing issue.

## Round 1 re-review

The clone identity check and `aborted` rollback-flag validation are fixed at
`installer_linux.go:227-235` and `:321-322`. New tests exercise transient root
replacement, all three terminal phase/image relationships, cross-session
snapshots, source replacement, injected deletion/cancellation/fence failures,
pending markers, and the nine-minute challenge case. Those changes address the
corresponding findings and the previously listed evidence gaps.

One part of the challenge-expiry finding remains. The new bound is
`RequestedAt + 10 minutes` (`installer_linux.go:575`), but the producer accepts
an imported request as old as five minutes before it begins staged artifact
verification (`native_linux.go:607-644`), then creates a challenge expiring nine
minutes after activation starts (`native.go:264`). A valid residual challenge
can thus expire later than `RequestedAt + 10 minutes`; the added test uses a
request only one second old and does not cover the accepted five-minute age.
Consider bounding the challenge relative to snapshot time (the challenge must
have been created before terminal capture) or otherwise covering the producer's
full accepted timing window. Verdict: the first and third findings are
addressed; the second is partially addressed and remains open.

## Round 2 re-review

The remaining expiry issue is addressed under the integrator's ruling. The
installer now requires the challenge expiry to be present, no earlier than the
journal request, and no more than ten minutes ahead of observation time
(`installer_linux.go:575-578`). This permits an expired historical challenge
and a later recovery challenge even when the activation request is old, while
bounding a newly future-dated value. The added tests cover the producer's
nine-minute lifetime, a three-minute recovery challenge with a twenty-minute
old request, a twelve-minute future expiry, and nonce mismatch
(`installer_linux_test.go:459-526`). I find this rule consistent with the
stated producer bounds and the root ruling. All three original findings are
closed; verdict: PASS for the scoped round 2 review. Native host qualification
remains outside this review.

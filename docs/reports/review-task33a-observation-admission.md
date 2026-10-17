# Task33A observation admission — independent scoped review

Reviewed exact source: `e6846d8..cc53500594cec483bf6a73aa1e4de335a8cfabc5`, against the frozen `task33a-observation-brief.md`. Reviewer confirmed that `.superpowers/sdd/2026-10-17-valheim-parity-completion/task33a-observation-review.diff` equals the candidate Git diff byte for byte.

**SPEC: PASS — QUALITY: APPROVE. Findings: 0.** This accepts the observation-admission source correction. Prior Task33A findings remain closed. Real hosted backup/restore and broader qualification remain open.

## Admission and concurrency review

`WithObservation` validates context, instance ID and callback, then shares the existing admission mutex with Enqueue, dispatch, mutation reservations and shutdown. It requires a started, resumed, nonstopping service, no same-instance idle/observation reservation and no queued/running same-instance job. The reservation and worker-drain count are installed together under that mutex. The callback runs after unlocking and after the admission query has released its SQL connection.

Enqueue retains its existing mutation-reservation refusal and can persist jobs during an observation. Dispatch checks the observation map before claiming work. Once a job is queued, subsequent observation admission sees its durable queued state and refuses, so new readers cannot repeatedly get ahead of accepted work. Other instances remain eligible for admission and dispatch; observations do not consume one of the four running-job slots.

The deferred release runs after callback cleanup, removes the reservation, signals dispatch and leaves worker drain. Cancellation does not remove the reservation while callback cleanup is still executing. Panic propagation also retains it through unwinding and then releases it. Shutdown closes admission and cancels the queue before its worker wait, preventing a new reservation from adding to a completed drain. An observation already admitted remains part of that drain; Stop reports its deadline while cleanup remains blocked. Callback contexts retain their existing I/O bounds; the implementation does not release early or detach callback cleanup.

`WithIdle` and `WithPausedInstance` now reject an overlapping observation. Existing queued/running mutation exclusion, paused recovery rules, execution authorization, session lifetime, native stopped-state checks and backup/save/restore reservations are otherwise unchanged. There is no new harness retry or relaxed backup guard.

## Five-callsite read-only audit

| Migrated caller | Verified callback behavior and retained guards |
|---|---|
| `console.Refresh` | Calls `session.verify(ctx, false)`: authenticated status/gamerule reads or fresh legacy `list`; cannot apply gamerule changes. Manager readiness-cache updates remain observations. Session/native-generation checks and command timeout remain. |
| `console.PlayerSnapshot` | Reads authenticated roster RPC or fresh legacy `list`; preserves bounded command context, identity/provenance, completeness checks and final generation verification. |
| `console.NativePlayerList` | Reads discovered allowlist/bans/operators RPC through `Session.PlayerList`; no mutation method or saved-file mutation is invoked. Session scope and native checks remain. |
| `console.PlayerPositions` | Reads roster and targeted `data get entity` Dimension/Pos/Dimension responses. Its four-second context, 16-player cap, captured-generation checks and incomplete/noncoherent-observation disclosures remain. |
| `players.Lists` saved fallback | Reads a validated list through a pinned server root, checks stable bounded file contents and compares native identity before/after. It preserves saved provenance and `RuntimeVerified=false`. |

None of these callbacks calls queue methods or mutates controlled server state. The source changes at these sites replace only their reservation call, plus the Refresh explanatory comment. Existing job-owned mutation sessions remain on their original path.

## Independent verification

Reviewer built a temporary Go overlay replacing the four caller source files with their **e6846d8** versions, retaining the candidate tests and queue implementation. This reproduced **all five admission failures**:

- `TestReadOnlyProbeAcceptsQueuedCommandDuringDelayedRPC`: `refresh`, `snapshot`, `list` and `positions` each failed with `instance_busy: instance is being changed` when the authorized command was enqueued during delayed authenticated RPC discovery.
- `TestSavedListObservationAcceptsQueuedWork`: failed with the same admission refusal during the delayed saved-list generation check.

The identical focused command without the overlay passed both packages (console 0.040s; players 0.012s). The overlay and old source copies were temporary; implementation files were not edited.

Reviewer then completed:

```text
go test -race ./internal/jobs ./internal/console ./internal/players -count=1
PASS: jobs 10.747s; console 12.618s; players 8.469s

go vet ./internal/jobs ./internal/console ./internal/players
PASS

git diff --check
PASS
```

The package checks cover durable admission while dispatch is deferred, retained payload, unrelated instance progress, queued/running work priority, mutation/observation exclusion, lifecycle/argument validation, returned errors, cancellation/panic cleanup, Stop timeout/drain and wakeup after release. Existing idle and paused-recovery regressions also pass, preserving ordinary mutation-admission refusal. Integration regressions exercise the real queue and authenticated in-process HTTP/WebSocket fixture, or a pinned local filesystem fixture; they do not execute Minecraft or native services.

Per the review scope, the unrelated full-suite `release/candidate.TestCandidateContract` VCS-status failure was not rerun or worked around. No full-repository passing claim is made.

## Remaining hosted evidence

The report accurately leaves the specific observer competing with backup in run `39038021693` unknown. The baseline reproductions establish the common structural admission conflict across the five audited paths. They do not establish that every later backup/restore phase succeeds. The integrator must review a new exact-source GitHub run for actual online backup, save-on/lease behavior, archive/restore/reconnect and native-platform evidence before accepting those capabilities or closing Task33A/Task33.

Only this review report was written. No implementation edit, staging/commit, dependency change, hosted dispatch, native service/account/firewall action, local game execution/connection or user-preview interaction was performed.

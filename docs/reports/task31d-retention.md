# Task31D1 — configurable automatic-backup retention

2026-10-17. Worker delivery against Minecraft baseline `8f40508`; integrator review and ledger acceptance remain pending. This packet implements the approved retention ruling, not Task31D2 running-configuration/restart behavior.

## Source and boundary

Read the approved Minecraft design, original implementation plan, implementation ledger, Task31 brief, and `.superpowers/sdd/2026-10-17-valheim-parity-completion/task31d-retention-brief.md`. The root ruling freezes the per-instance table, GET/PUT contract and zero-disables behavior. Read-only reference inspection confirmed Valheim HEAD `dabec19585e3f401eceb127840c71542fa84dac1`: `web/src/features/instances/InstanceConfigForm.tsx` exposes count/day values with minimum zero, and `internal/backup/service.go` retention treats each zero limit as disabled and combines positive limits with OR eligibility. Minecraft retains its own protected manual/safety scope, staged deletion journal and active archive references.

Worker changes are confined to `internal/backup/policy.go`, `policy_test.go`, retention implementation/tests, narrow policy admission and postpublication reporting in `service.go`, `web/src/features/backups/BackupsPage.tsx`, new `RetentionPolicyPanel.tsx`, `web/tests/backup-retention.mjs`, and this report. No preexisting browser fixture needed changes. Migration00019, HTTP handlers/API tests, OpenAPI schema and generated types are root-owned and were supplied during implementation.

## Delivered behavior

- Missing policy row means ten automatic archives/thirty days at revision0. Administrators can persist count0–10000 and days0–36500; zero disables the corresponding limit. Both zero values disable automatic pruning. Bounds constrain inputs, not storage or capacity guarantees.
- Mutation checks the current persisted administrator inside the policy/audit transaction. Numeric before/after policy values and revisions are recorded under `backups.retention_policy`; the audit and policy either commit together or roll back together. Conflicting stale edits return conflict; retrying an identical desired policy creates no extra revision/audit entry.
- Saving policy deletes no data. Manual and safety captures do not invoke pruning. A validated automatic capture reads the then-current policy after publication. Enabled count/age limits combine with OR eligibility, while manual/safety/protected archives, active downloads and active/queued restore references remain protected.
- A cancellable service admission gate serializes policy changes against policy reading, deletion decisions and admitted filesystem deletion. Gate admission precedes DB admission and the archive-reference mutex. DB connections are not held while waiting for the gate. A committed looser policy cannot coexist with still-running deletions admitted under an older policy.
- Invalid stored policy fails closed. A retention failure after successful publication leaves the archive ready and produces explicit safe job output explaining that the backup validated but retention failed, plus a `validated_retention_failed` result audit. Earlier capture/publication failures retain their original failed-state reporting. Staged deletion and recovery code remain intact.
- Backups show the current effective policy and protected scope to all roles. Administrator controls save or select default values, disclose zero behavior and next-automatic-backup timing, retain failed edits, and reload stale revisions while preserving edited values. Inputs validate whole numbers and bounds. Instance/account/role changes unmount and abort pending policy reads/actions; late responses cannot update the new context. Existing exact job links and explicit stopped/safety restore confirmation remain present.

## Verification evidence

All checks below ran on isolated fixtures in this workspace; no real world/server/service was exercised.

| Check | Result and coverage |
|---|---|
| `go test ./internal/backup -count=1` | PASS. Entire backup package, including existing capture/save-lease/download/restore regressions and new policy cases. |
| `go test -race ./internal/backup -count=1` | PASS,11.107s. Entire package under the race detector. |
| `go vet ./internal/backup` | PASS. |
| `npm run typecheck` from `web` | PASS against root-generated policy types. |
| `npm run lint` from `web` | PASS. |
| `node web/tests/backup-retention.mjs` against isolated Vite fixture port15231 | PASS: effective defaults, zero/zero save, default-values selection without immediate mutation, explicit save, preserved failed edits, stale conflict/reload/CAS retry, invalid number refusal, administrator versus operator/viewer controls, pending instance read/save cancellation, pending same-role account read cancellation and role-change save cancellation, keyboard tab order and390px overflow check. Zero page errors. |
| `git diff --check` | PASS at worker handoff. |

Backend fixtures use the actual migrated SQLite DB and temporary archive paths. Tests cover persistent policy through a new service object, defaults, input bounds/invalid instance, current-role/disabled-account rejection, atomic rollback on an injected audit failure, identical retry versus conflicting revision, positive/disabled count+age combinations and all candidate protection flags. Actual capture tests prove policy save does not prune, manual/safety captures do not prune, the next automatic capture applies policy, and a policy changed at the publication boundary is used. A corrupt stored policy blocks deletion while preserving a newly validated ready backup and honest job/audit output. Reference tests preserve active downloads, queued/running restore jobs and unfinished restore target/safety references; releasing those references permits deletion. A real filesystem deletion failure retains the `deleting` journal and recovery completes it. The synchronization test holds the existing reference mutex after retention admission and proves policy mutation cannot commit before admitted deletion completes; cancelled admission returns without mutation.

Observed red-to-green evidence: disabled-limit candidate tests originally deleted the newest backup for age-only/unlimited policies; they pass after enabled-limit handling. Policy API tests initially failed to compile because the service methods/types were absent, then failed on the not-yet-supplied table before root migration integration. The browser fixture initially timed out waiting for the absent effective-policy UI, then passed after integration. The explicit valid-backup job-output assertion failed against the original sanitized policy-only failure message and passes with the postpublication outcome message. An initial fresh-authority fixture hit the existing last-admin guard; adding a second fixture administrator permits exercising valid demotion/disable operations without bypassing that guard.

Screenshots from the final intercepted browser run were visually inspected:

- `/tmp/task31d-retention/retention-admin-mobile.png` —390px layout, saved effective values and keyboard-accessible inputs/actions.
- `/tmp/task31d-retention/retention-admin-desktop.png` — desktop current policy and administrator editor.

## Limits and integration status

The browser uses explicitly intercepted authentication, instance metadata and retention responses against the shipping UI. It proves UI behavior and context fences, not HTTP authorization, request rollback or real backup deletion; root owns HTTP integration verification. A cancelled browser request can already have reached the server, so server-side fresh authority, revisions and serialization remain authoritative. Policy and archive tests use temporary local files, including inert deletion fixtures; real archive validation remains exercised by existing capture/download tests.

No real Minecraft/client/version-layout matrix, installed service, native Windows retention behavior, EULA acceptance, external provider or stable support claim is inferred. No real backup data, user preview port12826, dependencies, credentials, commits or staging were changed. Root acceptance review, shared contract integration evidence and ledger update remain required; this worker does not accept Task31D or claim product completion.

# Independent re-review — Task31D2 UI fix round 1

Reviewed `cc2a13d..f6aef83` and the frozen `.superpowers/sdd/2026-10-17-valheim-parity-completion/review-task31d2-ui-fix1.diff`. Scope is the original three Important findings and regressions introduced by their fixes. Source changes are confined to `PendingSettingsPanel.tsx`, the focused browser fixture, and the implementation report; backend/API behavior is unchanged.

**SPEC: PASS. QUALITY: APPROVE. Zero open or new findings in this scoped re-review.**

## Findings closed

- **R1 — Closed.** Opening discard captures the reviewed draft's name and revision. Polling cannot advance the confirmation's CAS. The independently rerun `discard` probe opens revision3, receives replacement revision4, confirms using revision3, observes the409 while preserving the replacement, then explicitly closes/reopens and successfully confirms revision4. The dialog displays its captured target; conflict feedback remains visible.
- **R2 — Closed.** Polling and explicit refresh share one logical-read controller and generation. An explicit refresh cancels the older poll, including any applied-context refresh; polling skips a current read. Pending state, errors, CAS updates and loading completion check cancellation/current generation. Local successful mutations invalidate outstanding reads. Independently rerun `read` and `read-error` probes show that a superseded response cannot resurrect a consumed draft or restore its obsolete error; direct applied editing is correctly available after the newer no-draft response. Existing scope cancellation remains intact.
- **R3 — Closed.** Application is conditional on a valid base and current author authorization. Every existing draft now explains the apply-time authorization check, disabled/removed/demoted author refusal, explicit administrator resave/discard recovery, and the fact that current author status is not reported here. The conditional stale warning describes base mismatch only. The independently rerun `author` probe verifies disclosure for `stale:false`, without claiming detected revocation. The implementation report now accurately describes this boundary.

Reviewed the worker's retained original-code failure logs `/tmp/task31d2-red-{discard,read,read-error,author}.log`. Their failures match the original findings and the additional superseded-error case; candidate passes below were independently executed. Original review independently reproduced R1/R2 before these fixes.

## Independent verification

Own loopback Vite server: `cd web && npm run dev -- --port 15233`, bound to `127.0.0.1:15233`, stopped after verification. All browser API traffic was intercepted by the fixture.

| Command | Result |
| --- | --- |
| `cd web && npm run typecheck && npm run lint` | PASS, exit0 |
| `TASK31D2_PROBE=discard TASK31D2_URL=http://127.0.0.1:15233 node web/tests/pending-settings.mjs` | PASS, exit0 |
| `TASK31D2_PROBE=read TASK31D2_URL=http://127.0.0.1:15233 node web/tests/pending-settings.mjs` | PASS, exit0 |
| `TASK31D2_PROBE=read-error TASK31D2_URL=http://127.0.0.1:15233 node web/tests/pending-settings.mjs` | PASS, exit0 |
| `TASK31D2_PROBE=author TASK31D2_URL=http://127.0.0.1:15233 node web/tests/pending-settings.mjs` | PASS, exit0 |
| `TASK31D2_URL=http://127.0.0.1:15233 node web/tests/pending-settings.mjs` | PASS, exit0 |

The full fixture retains running draft save through the shipping CSRF client without active/lifecycle writes, complete/false/unknown config values, applied/desired distinction, direct-edit guards, error/conflict input retention, explicit stale re-save/base CAS, discard confirmation, consumed-draft active refresh, cross-instance/account/role delayed-request cancellation, viewer/operator restrictions, read failures,390px overflow and keyboard focus checks. No fix-introduced blocker was found in these covered interactions.

Limits: this approves the scoped UI source and intercepted interactions only. It does not qualify actual database persistence, server-side authorization, native services, games, or release compatibility. No user preview12826, database, accounts, services, games or firewall was accessed. No implementation edits, staging, commits or ledger acceptance were performed; only this assigned re-review report was written.

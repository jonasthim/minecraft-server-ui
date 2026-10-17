# Task31D2 — saved desired settings UI

Scope: `InstancesPage.tsx`, `PendingSettingsPanel.tsx`, `web/tests/pending-settings.mjs`. Integrator review and backend/native acceptance remain separate.

The overview identifies the last applied manager record and disclaims live game verification. A separate panel shows a saved draft, pending badge, revision/base, all ordinary configuration, a stale-base warning, and an unconditional explanation that current author authorization is checked at apply time. The `stale` response reports a base mismatch only; this UI does not infer or claim detected author revocation. The reused settings form supports name, memory and complete ordinary config including false booleans and additional properties. Port and existing world directory are disabled; EULA is excluded. Software, version and Java remain fixed. Seed text explicitly excludes world reset.

Saving only calls PUT pending-settings through the shipping typed API client and normal session CSRF. The panel explains ordinary manager start/restart, confirmed stop, the existing Server operations controls, excluded version update/rollback and direct OS autostart behavior. It does not add a lifecycle action. Direct applied editing is guarded while pending state exists or is unknown.

Draft forms retain user input after save errors, conflict, background polling and explicit base reload. A conflict prevents another save until explicit reload; reload preserves inputs and updates both CAS bases. A stale draft offers explicit re-save with the current applied base and an expandable current-applied comparison. Discard confirmation captures the exact reviewed name and revision. A replacement received while the dialog is open does not advance the confirmed CAS; a409 preserves that replacement and requires closing and explicitly reviewing/confirming again. A consumed draft refreshes the active context, clears obsolete pending-save feedback and shows the absence of a draft without claiming live-game success.

Instance/account/role scopes abort requests and unmount forms. Polling runs every ten seconds and skips any current read. An explicit refresh cancels an older read. The complete logical read, including applied-context refresh, uses one cancellation signal and generation; obsolete results, errors and loading changes are ignored. Successful local mutations also invalidate outstanding reads. Read failure remains visible and blocks editing; mutation failures survive successful background reads. Viewers/operators can inspect desired details but have no mutation actions.

## Verification

- `cd web && npm run typecheck && npm run lint` — PASS.
- Isolated server: `cd web && npm run dev -- --port 15232`, loopback only; every API request in the browser fixture is intercepted. The user's preview12826 and database were not accessed.
- `TASK31D2_URL=http://127.0.0.1:15232 node web/tests/pending-settings.mjs` — PASS (complete final fixture including account and role replacement).
- Browser assertions cover running save without active PUT/lifecycle calls; CSRF header; false booleans and unknown properties; desired/active distinction; blocked direct edit; failed save and409 preserved inputs; explicit stale re-save/base CAS; confirmed/cancelled discard; consumed draft active refresh; delayed reads/saves across instances and accounts/roles; viewer/operator visibility; read failure;390px width and keyboard focus. Account replacement is simulated through the existing logout handler; its modal case invokes the handler programmatically to change auth while focus is trapped.
- The first browser run failed because StrictMode's effect cleanup left a shared in-flight flag set, disabling initial preparation until the next poll. The flag was changed to effect-local state. This is a real test-discovered red/green defect, with the initial5s timeout at `Prepare desired settings` and subsequent complete browser success.

Screenshots (intercepted fixtures): `/tmp/task31d2-ui/pending-mobile.png`, `/tmp/task31d2-ui/editor-mobile.png`, `/tmp/task31d2-ui/viewer-mobile.png`. They depict390px flows; no actual game, native service, real backend, database, authorization enforcement or release qualification is inferred.


## Fix round 1 — independent review R1–R3

R1 captures `{name, revision}` when discard confirmation opens, displays that target in the dialog, and uses that revision for DELETE. A background replacement leaves the confirmation bound to the reviewed draft. The fixture's realistic409 response preserves the replacement; closing and reopening the dialog explicitly confirms its newer revision.

R2 gives every logical read its own abort controller and generation. Explicit refresh cancels prior polling, including any applied-context request; polling cannot replace an in-flight explicit read. Results and errors from cancelled or superseded reads are ignored. Existing local-mutation and instance/account/role guards remain. The additional error probe checks that an obsolete503 cannot restore an error after successful refresh.

R3 corrects the unconditional application promise and adds an always-visible note for every existing draft: the author is checked when applying, and a disabled/removed/demoted author blocks application until administrator re-save/discard. The note explicitly says author status is not reported. The conditional stale warning now describes only the known applied-base mismatch. No backend/API contract change was made.

### Red/green evidence

Against the original reviewed UI, the new shipping-client probes failed:

- `TASK31D2_PROBE=discard TASK31D2_URL=http://127.0.0.1:15232 node web/tests/pending-settings.mjs` — FAIL: confirmation expected revision3, actual4.
- `TASK31D2_PROBE=read TASK31D2_URL=http://127.0.0.1:15232 node web/tests/pending-settings.mjs` — FAIL: old draft visible1 after consumption/newer refresh, expected0.
- `TASK31D2_PROBE=read-error TASK31D2_URL=http://127.0.0.1:15232 node web/tests/pending-settings.mjs` — FAIL: obsolete failure visible1 after successful newer refresh, expected0.
- `TASK31D2_PROBE=author TASK31D2_URL=http://127.0.0.1:15232 node web/tests/pending-settings.mjs` — FAIL: author-check note absent for the backend's `stale:false` response shape.

All four exact commands PASS after the fixes. Logs are `/tmp/task31d2-red-{discard,read,read-error,author}.log` and matching `/tmp/task31d2-green-*.log`. Initial simultaneous cold-browser attempts for the two read probes hit a5s navigation timeout; those were rerun and produced the substantive failures above before implementation. The fixture now gives navigation30s for Vite/browser startup; its action/assertion timeouts remain5s and its poll observation remains bounded12s.

`cd web && npm run typecheck && npm run lint` — PASS after the fixes. The original complete fixture command also PASS after the probes, preserving the covered390px keyboard, CSRF, error/conflict, viewer/operator and account/role cancellation behaviors (`/tmp/task31d2-green-full.log`). The existing screenshots remain intercepted UI evidence only. No user preview/database, backend, services, games, dependencies, staging, commits or pushes are part of this packet. Independent integrator review remains required.

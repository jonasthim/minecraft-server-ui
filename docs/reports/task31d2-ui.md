# Task31D2 — saved desired settings UI

Scope: `InstancesPage.tsx`, `PendingSettingsPanel.tsx`, `web/tests/pending-settings.mjs`. Integrator review and backend/native acceptance remain separate.

The overview identifies the last applied manager record and disclaims live game verification. A separate panel shows a saved draft, pending badge, revision/base, all ordinary configuration, and stale authorization/base warnings. The reused settings form supports name, memory and complete ordinary config including false booleans and additional properties. Port and existing world directory are disabled; EULA is excluded. Software, version and Java remain fixed. Seed text explicitly excludes world reset.

Saving only calls PUT pending-settings through the shipping typed API client and normal session CSRF. The panel explains ordinary manager start/restart, confirmed stop, the existing Server operations controls, excluded version update/rollback and direct OS autostart behavior. It does not add a lifecycle action. Direct applied editing is guarded while pending state exists or is unknown.

Draft forms retain user input after save errors, conflict, background polling and explicit base reload. A conflict prevents another save until explicit reload; reload preserves inputs and updates both CAS bases. A stale draft offers explicit re-save with the current applied base and an expandable current-applied comparison. Discard has confirmation and revision CAS. A consumed draft refreshes the active context, clears obsolete pending-save feedback and shows the absence of a draft without claiming live-game success.

Instance/account/role scopes abort requests and unmount forms. Polling is bounded to one in-flight poll per scope, every ten seconds. Mutation generations prevent an older pending read from replacing a completed local save. Read failure remains visible and blocks editing; mutation failures survive successful background reads. Viewers/operators can inspect desired details but have no mutation actions.

## Verification

- `cd web && npm run typecheck && npm run lint` — PASS.
- Isolated server: `cd web && npm run dev -- --port 15232`, loopback only; every API request in the browser fixture is intercepted. The user's preview12826 and database were not accessed.
- `TASK31D2_URL=http://127.0.0.1:15232 node web/tests/pending-settings.mjs` — PASS (complete final fixture including account and role replacement).
- Browser assertions cover running save without active PUT/lifecycle calls; CSRF header; false booleans and unknown properties; desired/active distinction; blocked direct edit; failed save and409 preserved inputs; explicit stale re-save/base CAS; confirmed/cancelled discard; consumed draft active refresh; delayed reads/saves across instances and accounts/roles; viewer/operator visibility; read failure;390px width and keyboard focus. Account replacement is simulated through the existing logout handler; its modal case invokes the handler programmatically to change auth while focus is trapped.
- The first browser run failed because StrictMode's effect cleanup left a shared in-flight flag set, disabling initial preparation until the next poll. The flag was changed to effect-local state. This is a real test-discovered red/green defect, with the initial5s timeout at `Prepare desired settings` and subsequent complete browser success.

Screenshots (intercepted fixtures): `/tmp/task31d2-ui/pending-mobile.png`, `/tmp/task31d2-ui/editor-mobile.png`, `/tmp/task31d2-ui/viewer-mobile.png`. They depict390px flows; no actual game, native service, real backend, database, authorization enforcement or release qualification is inferred.

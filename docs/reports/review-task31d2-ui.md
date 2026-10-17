# Independent review — Task31D2 UI

Reviewed frozen `6227cb2..00c83ca` and `.superpowers/sdd/2026-10-17-valheim-parity-completion/review-task31d2-ui.diff`, the UI brief, implementation report, approved design, original/corrective plans and ledger. Owned review scope: `InstancesPage.tsx`, `PendingSettingsPanel.tsx`, `web/tests/pending-settings.mjs`, `docs/reports/task31d2-ui.md`. Backend/API inspected only to establish the contract; its separate oversized-draft correction is outside this verdict.

**SPEC: FAIL. QUALITY: REQUEST CHANGES. Three Important findings.**

## R1 — Important: background polling changes the revision an open discard confirmation authorizes

Location: `web/src/features/instances/PendingSettingsPanel.tsx:64` and `:92` / `:106`.

Opening the discard dialog records only `discarding=true`. Its eventual DELETE reads the latest `state.revision`, which background polling can replace while the dialog is open. Consequently the confirmation can delete another administrator's replacement draft without a conflict or review of that draft. The request contains CAS, but silently advances that CAS to the unreviewed revision.

Reachable reproduction using the shipping client and intercepted APIs: load beta revision3, click **Discard saved draft**, replace the backend fixture with revision4 and a different desired name, wait one ten-second poll, then click **Confirm discard draft**. The request sends `expected_revision:4`, succeeds, and removes the replacement draft. `/tmp/task31d2-review-discard.mjs` exits1 on the assertion that the confirmation must retain revision3; diagnostic output is `confirmation opened at revision3, DELETE expected_revision = 4 remaining draft = null`.

Capture the revision being confirmed when opening the dialog. If it changes, retain that CAS so the backend refuses, or explicitly invalidate the confirmation and require review again. Add a regression covering another administrator's save while confirmation remains open.

## R2 — Important: an older poll can overwrite a newer explicit read

Location: `web/src/features/instances/PendingSettingsPanel.tsx:26`–`:40` and `:45`–`:56`.

`generation` changes after local mutations only. Periodic polling and explicit reload have independent in-flight control, so both reads can share a generation and commit out of order. This makes the panel display obsolete desired data after the user has explicitly refreshed. It also changes the direct-edit guard and supplies obsolete revisions/content to a subsequently opened editor.

Reachable reproduction: hold a background GET returning revision3 with a draft; consume that draft externally to revision4/draftnull; click **Refresh saved settings** and observe **Prepare desired settings**; release the old GET. **Beta desired** reappears. `/tmp/task31d2-review-race.mjs` exits1 with `older poll must not resurrect consumed draft`, actual visible old-draft count1, expected0. This is within one instance/account/role and requires no local mutation. Existing cross-scope cancellation and mutation generation tests do not cover it.

Order/serialize pending reads across poll and explicit reload, or reject superseded results. Protect failures from obsolete requests as well. Add the delayed-poll/newer-refresh regression, including draft consumption.

## R3 — Important: author revocation is presented as covered by a flag that cannot report it

Location: `web/src/features/instances/PendingSettingsPanel.tsx:77`–`:85`, `:96`; implementation report's stale authorization warning claim.

The persistent explanation says the next ordinary start/restart applies the draft. The only author-revocation explanation is conditional on `state.stale`. Actual backend `readPendingSettings` sets `Stale = BaseUpdatedAt != current` (`internal/instance/pending_settings.go:137`) and performs no author authorization lookup. A saved draft whose author is disabled, removed, or demoted while its base remains unchanged therefore still returns `stale:false`. Applying it separately calls `freshPendingAdmin` (`:335`), refuses the start, and requires another administrator to resave/discard. This is a concrete contract distinction, not an assumed backend behavior.

The shipping-client fixture with the backend's unchanged-base response displays zero author-revocation warnings (`/tmp/task31d2-review-race.mjs`: `blocked-author warning count = 0`). The current fixture's forced `stale=true` tests a different state and cannot prove revoked-author disclosure. The UI also never displays `updated_by`, so it cannot independently infer author validity.

Make the ordinary explanation accurately conditional on current author authorization and explain the explicit resave/discard recovery even when `stale:false`. Do not claim this flag detects revoked authors. If a detected blocked state is desired, ask the integrator for an explicit contract rather than guessing from `updated_by`. Update report/test coverage to reflect the actual contract.

## Verification and accepted observations

- `cd web && npm run typecheck && npm run lint` — PASS, exit0.
- Own Vite: `cd web && npm run dev -- --port 15233` — bound to `127.0.0.1:15233`; stopped after review.
- `TASK31D2_URL=http://127.0.0.1:15233 node web/tests/pending-settings.mjs` — PASS, exit0.
- `TASK31D2_URL=http://127.0.0.1:15233 node /tmp/task31d2-review-race.mjs` — FAIL, exit1, reproduces R2; also verifies the R3 warning absence against the actual backend response shape.
- `TASK31D2_URL=http://127.0.0.1:15233 node /tmp/task31d2-review-discard.mjs` — FAIL, exit1, reproduces R1.
- Both additional probes derive the supplied fixture's setup/routing and run the shipping client; only temporary `/tmp` scripts were created. Every API request is intercepted. No implementation file was changed.
- Reviewed generated screenshots `/tmp/task31d2-ui/editor-mobile.png` and `/tmp/task31d2-ui/pending-mobile.png`. The 390px dialog/panel fit their viewport; the fixture verifies no horizontal page overflow and keyboard Tab movement from name to memory. This is focused keyboard evidence, not a full accessibility audit.
- Draft save uses the typed normal CSRF client and only PUT pending-settings; active PUT and lifecycle mutations are absent from the running-save fixture. Complete config, false booleans and unknown properties survive. Port/world/runtime/version/EULA are excluded from editing. Viewer/operator actions are absent. Conflict/error inputs persist, explicit base reload preserves edits, and cross-instance/account/role requests are cancelled. No additional blockers found in these covered paths.

Scope limits: intercepted UI/source review only; no native game/service, actual persistence or authorization-enforcement qualification. The user's preview12826, database, accounts, services, games and firewall were not accessed. No staging/commit or ledger acceptance was performed. Integrator review and closure of R1–R3 remain required.

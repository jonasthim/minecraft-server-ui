# Task31B — content and pack browsing / dependency guidance

2026-10-17. Source packet based on `097c743`; acceptance remains with the integrator. Owned changes only: `web/src/features/content/**`, `web/src/features/modpacks/**`, `web/tests/content-workflows.mjs`, this report. No API/schema/backend, shared shell/router, dependencies, services, game/EULA actions, staging or commits.

## Delivered behavior

- Content and pack project/release browsing now has first/previous/next controls. Response `offset`, `limit`, `total` and fetch timestamp determine bounds and scope labels. A page replaces the previous page. The submitted search text is retained for paging even when the input is edited. Empty and final pages stop forward paging.
- Content discovery carries the exact installed Minecraft/loader tuple and mod/plugin kind. Pack browsing deliberately retains its existing unfiltered publisher scope (the pack snapshot has no installed tuple); visible copy discloses that pack compatibility is evaluated by the existing strict server-bound preview. No publisher metadata becomes qualification evidence.
- Discovery aborts superseded requests and discards their responses, with reset on provider/tuple change and component remount on instance/account/role change. Provider switching remains usable during a catalog request. Mutation/job gates remain in place. Loading status is visible.
- Required dependency entries now show exact publisher/project/release identities and matching installed manifest identities, including enabled state. The operator can explicitly open an exact required release or page compatible releases for an exact project when the publisher did not pin a release. Each selected file still uses the existing strict preview and explicit stopped-server apply. An existing dependency can be updated; a disabled matching dependency has an explicit enable preview.
- The guided stack saves the parent mutation, project, release-page offset and any exact-version restriction. Returning refreshes the manifest and builds a fresh parent preview. It never silently applies the parent, installs dependencies, clears blockers, or derives identity/compatibility from a name or filename. Optional/informational dependencies and incompatible declarations are distinguished; cycles and missing publisher identities are explained without a guessed install action. Exact releases that do not declare the installed tuple remain blocked with an explanation; backend preview/apply checks remain authoritative.
- Pack job polling now consumes the frozen `{job: Job}` envelope. Both owned pollers serialize reads, refresh before their terminal state update, preserve terminal state even if refresh fails, and stop on terminal state. Pack failure detail is visible. Existing exact `jobHref(job.id)` links are preserved.

## Evidence

Commands run successfully against this packet:

```text
npm --prefix web run typecheck
npm --prefix web run lint
node web/tests/content-workflows.mjs
git diff --check -- web/src/features/content web/src/features/modpacks web/tests/content-workflows.mjs
```

The test uses the shipping React/Mantine application through isolated Vite port `15431` and intercepted API fixtures in Chromium at 390×844. It does not use port `12826`, seed a manager database, or contact a provider/game. Its initial red run timed out waiting for the absent `Next projects` control; implementation then made the regression pass.

The focused browser checks cover:

1. More than one project and release page for content and packs; server-returned limit 2 overrides requested limit 20, final/empty bounds, no appended duplicate rows, and submitted search preservation.
2. Held page responses released after provider, installed tuple, instance, and role changes; old results do not appear. Plugin requests retain the new Minecraft/Paper tuple.
3. Keyboard activation of exact publisher dependency navigation, explicit dependency preview/apply, fresh installed identity read, exact saved parent mutation on return, and continued parent blocking for an unresolved identity. Optional project releases remain browsable; missing identities/cycles/incompatible declarations have no invented install action. An exact mismatching dependency release cannot be previewed. A disabled installed dependency uses an explicit enable preview/apply, then returns to the same parent selection.
4. Pack strict-preview refusal, queued→succeeded and queued→failed job-envelope reads, visible failure detail, exact job link, and no more polling after terminal state (browser clock advanced six seconds).
5. Viewer browsing has no content mutation buttons and pack preview remains disabled. Role reset clears previews/jobs and rejects a held old page. No browser page errors or horizontal overflow at 390px.

Review screenshots: `/tmp/task31b-dependency-mobile.png` and `/tmp/task31b-pack-mobile.png`. These are intercepted interaction evidence only. The browser script regenerates them.

## Boundaries and remaining qualification

This implements the approved explicit guided workflow, not an automatic multi-project dependency resolver. A version-only declaration without `project_id` cannot use the current project-scoped exact-version route; a name-only descriptor dependency also lacks a publisher identity. These stay blocked/explained. A future resolver needs an exact version-to-project API and authoritative publisher-to-descriptor identity/range evidence; filename/name matching is not a substitute. An uploaded parent may require restaging/reverification if its staged upload expires before returning.

Publisher access, successful real addon/pack installation, descriptor compatibility across the release matrix, and actual game/server operation remain Task33 qualification. No support combination or real provider/pack/game result is claimed. Integrator review and ledger acceptance are still required.

## Frozen source for independent review

```text
e7326095e78c2fec58adfe012ca8ce1717ae29978220b37c44ac137cdbf41905  web/src/features/content/ContentPage.tsx
abdc0e6df089680941c3b540c073f6aab71170f7f2a12cbc238a33e623991cbe  web/src/features/content/CatalogBrowser.tsx
e6eaf7648ccb0c1f5bd22810c09659a2a682ac120061bc113a84cad3372d8d89  web/src/features/modpacks/ModpacksPage.tsx
0e6b4539198460904c99ce6820c069bdc9ae0530379764062a479a7531459c6c  web/tests/content-workflows.mjs
```

## Review fix round 1 — retained parent preview lifecycle

2026-10-17, integrator baseline `c863c63`. The reviewer identified that entering a dependency discarded the browser's parent preview reference without deleting the server row. That retained blocked row could consume the 32-preview allowance and block automatic updates for its retention period.

`dependencyPlan` now saves the exact parent mutation/project/page/exact-release context locally, then deletes the original preview through the existing authorized endpoint **before** changing navigation state. A failed delete displays the API failure and keeps the original preview, selection and dependency action available for retry. A successful delete enters the same guided path; returning still creates a fresh strict preview from the saved mutation. No backend contract or trust/compatibility gate changed.

The intercepted fixture now retains preview rows in a map, derives manifest automatic blockers from those rows, deletes rows only for actual DELETE requests or successful apply completion, and can reject preview deletion. The new regression checks that deletion failure retains the server row and navigable parent without a dependency lookup; retry removes the original server row; after explicit dependency and parent completion, no preview rows or automatic blockers remain. Its red run failed waiting for the deletion failure alert because the old implementation never issued DELETE. The fixed run passed.

Narrow verification only:

```text
CONTENT_PREVIEW_LIFECYCLE_ONLY=1 node web/tests/content-workflows.mjs  # passed
npm --prefix web run typecheck                                    # passed
npm --prefix web run lint                                         # passed
git diff --check -- web/src/features/content web/tests/content-workflows.mjs docs/reports/task31b.md
```

The lifecycle-only mode exercises the existing content dependency checks and the new retained-state lifecycle; it skips pack, provider-delay and role cases already recorded above. Evidence remains intercepted shipping UI logic, without real provider/game qualification. Used isolated Vite `15431`; user preview `12826` was not touched. No staging or commit.

Updated frozen hashes (supersede the corresponding original entries above):

```text
4589ed1c97d9ace71044def6d361a3c3def1c3193924116fa0be19616ced712c  web/src/features/content/ContentPage.tsx
05833565a544707b659e5bc60c265d03cfb8912f8d3b6d8a84502193ed5d0806  web/tests/content-workflows.mjs
```

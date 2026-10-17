# Task31A: Jobs, audit details and known-player actions

2026-10-17. Submitted for integrator review; this report does not mark the ledger accepted. Scope is Task31's jobs/audit/players packet under the approved Minecraft design and parity plan. Existing generated contracts suffice; no API, backend, dependency, schema, router or shared shell edits were made by this worker.

## Delivered behavior

- `/jobs?job=<encoded ID>` opens the exact job in an accessible Mantine drawer. It requests the authorized job directly even when absent from the newest 100 list or excluded by the list filters. The list offers state and exact-instance filtering with explicit loaded-history scope and distinct filtered-empty, initial-empty, loading and failure/stale states. No unsupported job pagination is implied: the current backend list is capped at 100.
- Reused job details retain durable ascending log cursors, bounded newest 500 display, redacted full-log download, authorized SSE reconnect, terminal stream closure/final output recovery, and existing owner/operator/admin cancellation rules. Detail paths encode IDs; account/role/job changes reset requests, logs and action state. Reads have 10-second deadlines. The jobs list and activity use the existing visibility-aware serialized polling helper at 10 seconds.
- Exported `JobActivity` provides loading/unknown/error handling, a concise sidebar button, a contextual drawer over up to 100 recent authorized jobs, ten selectable recent entries, and exact-job navigation. It labels the loaded history boundary rather than asserting a complete activity count. The integrator mounted it in shared navigation and owns producer link updates.
- Audit shows the recorded server-redacted `details` object through native keyboard-accessible disclosure and escaped JSON. It invents no unrecorded before/after fields. Text filtering explicitly applies to loaded rows; retained history pages by the existing `before` cursor. Loading/failure/empty/end-of-retention feedback is distinct, concurrent paging is disabled, previous rows on failed refresh are labelled potentially stale, and non-administrators cannot render retained audit state.
- Operators/admins can copy an observed online, native-list or historical session identity into the existing action form. It copies known name/UUID and provenance, clears the previous UUID for name-only records, disables unknown identities and preserves all existing server/version/role mutation rules. Prefill does not submit an operation. Instance/account/role changes remount controls, clear prior identities/jobs/actions and abort outstanding mutations/history loads. Player operation links now target their exact job.

## Frozen integration contract

From `web/src/features/jobs/index.ts`:

```ts
jobHref(jobID: string): string // /jobs?job=<encodeURIComponent(jobID)>
JobActivity(): JSX.Element // mount inside authenticated Shell; no props
JobDetails({ jobID, onUpdate }: {
  jobID: string;
  onUpdate: (job: components['schemas']['Job']) => void;
}): JSX.Element
JobsPage({ instanceID?: string }): JSX.Element // existing optional scope retained
```

Root owns shared Shell placement and exact-ID updates in lifecycle/console/backups/content/packs/schedules/updates. The activity drawer is independent of feature routes and resets on account/role change. Exported details also reset themselves for job/account/role changes.

## Verification

- `npm --prefix web run typecheck`: PASS.
- `npm --prefix web run lint`: PASS.
- Started only the assigned fixture Vite server: `npm --prefix web run dev -- --host 127.0.0.1 --port 15331 --strictPort`.
- `node web/tests/daily-workflows.mjs`: PASS against the shipping routed UI at 390×844 using intercepted generated-contract responses. The test checks an exact job outside recent results, zero terminal SSE constructions, state/instance filter truthfulness, a backend 403 without cancellation access, initial activity loading and mobile contextual drawer navigation, escaped HTML-like recorded audit detail via keyboard, actual `before=101` paging after 100 records, filtered-empty versus globally-empty distinction, stale/error behavior, online UUID and historical name-only prefill, unknown-identity refusal, the exact mutation payload without an inferred UUID, exact originating job link, same-document instance switching clearing action state, role transition removing mutations and page-width bounds. Browser page errors: zero.

The focused fixture suite is deterministic UI regression evidence, **not** successful real Minecraft/player/audit/provider qualification or a first-user success claim. Existing actual-empty/admin/create evidence is already recorded in Task 25/Task 26; it was not recreated with fixture seeds. No game, EULA, service, user data, production credential, commit, staging, push, dependency or vault write occurred. Integrator review and real-game/native acceptance remain separate gates.

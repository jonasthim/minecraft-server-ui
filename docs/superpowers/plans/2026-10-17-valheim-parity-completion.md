# Valheim Parity Completion Plan

> **For agentic workers:** Continue the approved implementation using `superpowers:subagent-driven-development`; the integrator owns shared wiring and acceptance. Steps use checkbox syntax. This is a corrective continuation, not a replacement product design.

**Goal:** Finish the approved Minecraft adaptation to the public Valheim product's daily-use and installed-delivery standard.

**Architecture:** Retain the reviewed native Go/SQLite and React/Mantine application. Complete missing workflows using existing typed contracts where sufficient, then qualify the actual installed product on disposable native hosts before stable publication.

**Tech Stack:** Go 1.27.0, React 19, Mantine, strict TypeScript, existing Playwright, native systemd/Windows SCM, managed Adoptium Temurin. No new dependencies without integrator review.

**Spec:** `docs/superpowers/specs/2026-10-16-minecraft-server-ui-design.md`; original plan `2026-10-16-minecraft-server-ui.md`. Reference public Valheim v1.21.3 / `dabec19585e3f401eceb127840c71542fa84dac1`. Baseline audits: `docs/reports/valheim-product-parity.md` and `valheim-delivery-parity.md` at Minecraft `c13fb51`.

## Global Constraints

- The original approved design and original plan's Global Constraints remain binding, including Java-only/version floors, per-instance isolation, five-minute online save lease, protected backups, strict pinned updates, global roles and redacted audit.
- Exclude Bedrock, Docker, remote hosts, existing-server imports, world uploads, remote backups, mandatory add-ons, and client pack export.
- Retain observed/unknown/stale/provenance distinctions. JVM measurements are not host measurements; failed or incomplete reads never imply zero players or a healthy server.
- Keep the 390 px layout, keyboard navigation and accessible feedback. Use Minecraft styling while restoring the reference's useful operating structure.
- Do not weaken native launch seals, role gates, confirmation, secret handling or publisher verification to make a test or installer pass.
- Install test services only on disposable GitHub runner VMs. Do not change services, accounts or worlds on the user's own host. No real game launch before explicit EULA consent; no production trust/key creation or stable publication without the required owner decision.
- Source review, package CI, installed-manager checks, fixture browser checks and real game/client qualification are distinct evidence classes. None silently substitutes for another.

## Review Focus

1. Navigation between instances or roles must cancel old requests and never display another instance's stale data/actions (Task26).
2. Missing telemetry, partial fleets and gaps must remain visible in counts/charts; overlapping polls must remain bounded (Task26).
3. Console exports/filtering must preserve redaction, ordering, limits and disclosed gaps without arbitrary filesystem access (Task27).
4. Installed manager restrictions must actually permit its narrow helper/SCM behavior; login-shell sudo and mocked services are insufficient (Task28).
5. Installation/update/uninstall must preserve existing data/config/trust and restore a runnable manager on failure (Tasks29–30,32–34).

## Ownership and ordering

| Task pair | Shared boundary | Decision |
|---|---|---|
| 26/27 | Instance routing versus console feature | 26 supplies `InstanceLayout` using React Router Outlet; integrator nests existing routes. 27 owns only console components/tests; shared shell/CSS remain26. |
| 26/31 | Global/instance shell versus feature polish | 31 starts feature edits after26's shell contract freezes; it does not edit the shell. |
| 28/30 | Native units/installers | 28 owns the first installed smoke and necessary unit/startup fixes. Freeze/review before30's installer lifecycle rewrite. |
| 29/30/32 | Artifact and signed metadata contract | 29 produces archives matching the shipping verifier and a separate authenticated bootstrap bundle contract; consumers must use that frozen contract. |
| All/API | Generated API/router/build | Integrator merges schemas, regenerates types and owns shared wiring. Workers propose exact changes rather than editing another package. |

### Task26: Daily operating shell, instance overview and trends

**Own:** `web/src/layout/**`, `web/src/features/instances/**`, `web/src/features/metrics/**`, `web/src/index.css`, `web/src/main.tsx` if needed; dedicated browser test/report. Integrator owns `web/src/App.tsx` route integration.
**Interfaces:** existing instance list/detail, lifecycle, player snapshot and metrics contracts listed in the product audit. Produce `InstanceLayout` with Outlet and an exported typed instance context for detail refresh. Retain the reviewed LifecyclePanel and its terminal refresh behavior.
- [ ] Add shared instance identity, readiness/process distinctions, lifecycle access and section navigation across all nested instance pages; keyboard/mobile section switching and selected-server navigation in the shell.
- [ ] Add explicit persisted theme control and live server cards/summary using bounded cancellable polling, visible partial/stale states and no fabricated fleet totals. Provide useful links/actions through existing authorized lifecycle behavior.
- [ ] Add accessible JVM/resource/player trend charts using existing data, with gaps breaking lines and a retained readable table. Do not add a chart dependency or invent host metrics.
- [ ] Verify actual empty-database admin/create/navigation/theme and390px/desktop behavior; use separately labelled intercepted telemetry only for running/gap/stale transitions. Run typecheck/lint and focused browser regressions; report reviewable screenshots and commands.

### Task27: Usable retained console

**Own:** `web/src/features/console/**`, dedicated tests/report. Consume existing authorized redacted console pagination/stream contracts; request any genuinely missing backend contract from integrator.
- [ ] Implement text filtering, follow-to-tail with user-controlled pause, semantic line presentation and bounded retained-history browsing/download with truthful scope/gap labels.
- [ ] Preserve live reconnection, cursor ordering, bounded memory, role-aware commands, redaction and server-change cleanup. Do not expose arbitrary raw log paths or imply a partial export is a complete log.
- [ ] Exercise actual empty/failure console plus deterministic delayed/reconnected/filter/download/gap cases at390px; meaningful tests must catch wrong-instance exports and unwanted scrolling. Run typecheck/lint and targeted browser checks.

### Task28: Installed-manager native CI and concrete startup fixes

**Own:** new `deploy/qualification/**` harnesses, necessary `deploy/linux/minecraft-ui.service` correction, dedicated report. Integrator wires a new manual native install workflow for Ubuntu22.04/24.04 and WindowsServer2022; current package CI remains.
- [ ] Build and install the exact shipping manager on disposable runners, starting from empty canonical state. Linux proves helper enable/disable from inside the actual manager service restrictions; Windows proves actual SCM startup under a dedicated unprivileged test account and private ACLs.
- [ ] Verify real setup API identity, first-admin persistence, stop/start/manager restart, no orphan processes, invalid helper input refusal, and scrubbed diagnostics. No game/EULA/client is involved in this first packet.
- [ ] Resolve demonstrated startup/unit/permission defects with scoped regression evidence. Investigate the Valheim-documented older-systemd implicit NoNewPrivileges conflict; keep game namespace restrictions and narrow sudo permissions intact.
- [ ] Run/review actual hosted results. Record the precise installed-manager scope; do not mark real game/service lifetime or other OS families qualified.

### Task29: Reproducible release candidates and authenticated artifact contract

**Own:** `release/**` producer/tests (preserve evidence gate), dedicated report; integrator owns build/workflow wiring.
- [ ] Produce version-stamped Linux/Windows manager update archives matching the existing signed-manifest verifier, plus manager/updater/deploy/install/license assets for installation. Define bounded authenticated bootstrap metadata covering every privileged asset.
- [ ] Sign/verify with disposable fixture keys in tests; accept release-owner signing input without generating or embedding production secrets. Verify packaged artifacts using shipping verifier code and rejection cases.
- [ ] Retain candidate artifacts in CI, clearly labelled as development evidence; keep stable publication disabled until qualification and owner authorization.

### Task30: Complete installer lifecycle and operator recovery

**Own:** native installers after28 freezes, narrow admin-recovery implementation and operator docs; integrator owns command wiring. Depends29 artifact contract.
- [ ] Implement verified pinned/latest-stable acquisition, clean install, preflight/check, data-preserving repair/rerun/upgrade/uninstall, service startup/readiness and rollback on failed manager activation. Preserve reviewed trust and config; never overwrite a live Windows image or stop Linux games for manager replacement.
- [ ] Complete Windows account/logon bootstrap and updater/probe lifecycle within the reviewed credential model. Provide safe account recovery and stopped-manager SQLite/WAL backup/migration instructions with exact tested commands.
- [ ] Exercise install→configure→rerun→upgrade/failure→uninstall on disposable native hosts and write usable operator install/proxy/backup/recovery documentation.

### Task31: Remaining daily-use workflow gaps

**Own:** disjoint feature packets for content/packs, jobs/audit/players, and map/events/schedules. No shared shell changes.
- [ ] Complete content and release pagination, explicit dependency planning within strict compatibility, exact-job/contextual access, recorded audit change details and known-player action prefill without inventing identity.
- [ ] Add useful schedule presets, retained event/history filters and direct map navigation; keep version/provenance/unknown behavior and mobile keyboard access.
- [ ] Adjudicate running-config/restart and configurable retention against existing safety rules before implementation. Preserve protected backups and confirmation; do not silently copy incompatible Valheim behavior.
- [ ] Verify successful shipping interactions and failure/role/empty states; keep real addon/pack/game acceptance in33.

### Task32: Stable manager release discovery and update delivery

**Own:** `internal/selfupdate` discovery and corresponding UI; integrator owns schema/API/wiring. Depends29.
- [ ] Add bounded latest-stable discovery, version comparison, notes and check/availability UI with caching and network-error feedback. Discovery never activates; signed exact-version verification and explicit confirmation remain required.
- [ ] Exercise genuine installed candidate activation and failed-health executable rollback/recovery on disposable native hosts. Keep unchanged-schema restriction until a separate compatibility design exists.
- [ ] Keep optional automatic manager activation disabled pending explicit policy resolution; this does not block manual verified delivery.

### Task33: Real first-server and compatibility qualification

**Own:** acceptance harnesses/evidence; coordinate native, provider, backup and browser owners. Depends working installed path and explicit EULA consent/client availability.
- [ ] From empty state use shipping onboarding/admin/instance/install/start, connect a real client, capture online backup/save-on, deliberately stop, explicitly restore with fresh safety backup, restart and reconnect on Linux and Windows.
- [ ] Expand exact tested tuples/layouts to the approved1.12/1.16/1.18/1.20.5/1.21.9MSMP/26.1/26.3 matrix and supported flavors/OS families. Exercise watchdog/crash/competing-write/update recovery and authorization failures.
- [ ] Publish truthful reviewed evidence per exact candidate/platform/tuple, fix discovered defects and retain unsupported boundaries explicitly.

### Task34: Final parity acceptance and stable release

- [ ] Reconcile both parity audits against actual delivered workflows and original approved exclusions; no missing accepted capability is reclassified as merely a qualification gap.
- [ ] Configure owner-controlled production signing/trust only after the concrete artifact/bootstrap process is reviewable. Link qualified evidence into the machine gate without fabricating stable-version evidence from development checks.
- [ ] Request final publication authorization only after artifacts, tests and release notes are concrete; publish stable release and verify public installation and prior-version upgrade after authorization.

## Completion rule

Repository creation, reviewed source and green package CI are intermediate milestones. Product completion requires the missing daily-use and delivery capabilities above plus their acceptance evidence. Continue unblocked work while preparing any genuinely necessary EULA, native-host, client, signing-key or publication decision for the owner.

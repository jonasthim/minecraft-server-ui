# Task 24 — manager self-update and release groundwork

Status: implementation submitted for integration/independent review; native release qualification and Task 24 acceptance remain pending. Product version remains `0.0.0-dev`. No production repository/key, stable artifact, signing credential or completed native qualification is invented. No service was installed, no game started, no EULA accepted, no external message sent, no release published, and this worker staged/committed nothing.

## Implemented contracts

`internal/selfupdate` verifies exact-byte Ed25519-signed bounded `release.json` with detached `release.sig` (`key_id`, base64 signature). Trust is an explicitly configured repository and public-key ring, loaded from a protected fixed native path. Empty/missing trust disables downloads and activation. Metadata binds stable strict `vMAJOR.MINOR.PATCH`, repository, bounded validity, updater format 1, both x64 native platforms, archive sizes/SHA-256 and raw executable sizes/SHA-256. Stable versions must increase. Development builds may verify/stage configured releases but cannot activate them.

Only unchanged schemas are eligible. Schema-changing updates require explicit manual maintenance; future schema-changing releases are not silently supported. The old executable is never restored onto a schema it cannot read. New `db.LatestVersion` and `db.CurrentVersion` expose embedded/existing schema versions without migration. Fixed CLI probes never open the manager for serving or migrate a missing database.

Downloads begin at the configured GitHub repository's fixed release path. At most three redirects may stay on the exact initial URL or move to `release-assets.githubusercontent.com`; no redirect back, other host, HTTP, credentials, fragment, custom port or proxy is accepted. Each network dial resolves/rejects private, loopback, link-local, multicast, reserved and non-global destinations before dialing an approved literal IP with normal TLS verification. Requests, response sizes, staging and decompression are bounded. Redirect queries/errors are not logged. [GitHub's asset documentation](https://docs.github.com/en/rest/releases/assets) explicitly requires handling streamed or redirected assets.

Archives must contain exactly the expected root executable, `LICENSE`, and `THIRD_PARTY_NOTICES.txt`. Unexpected/traversing/duplicate/link/special members fail. The archive and extracted binary have independent signed hashes. Staging uses checked roots, exclusive locks, private temporary files, file/directory durability fences, immutable hash-derived stage IDs and an authorization recheck before publication. Two retained stages bound storage. Stale staging locks and conflicting requests require explicit reconciliation.

Administrator-only handlers expose status, version check, staging and explicit confirmed activation. Shared authentication/CSRF mounting remains root-owned. Stage/activation mutations require fresh current enabled-admin DB authorization and safe audit records. Native requests preserve actor ID/time and stage ID durably; the privileged worker binds an imported request to its protected journal/challenge and rechecks actor authorization through the old manager-account probe immediately before executable replacement.

The root-owned native transaction copies and verifies prior/current executable bytes before replacing the fixed target, records durable phases, stops the manager explicitly, waits for the manager lock and complete drain receipt, checks live schema/actor, starts the candidate and requires authenticated exact version/schema/running-image SHA-256 readiness. Failed candidate readiness restores the captured executable only and performs one bounded prior-manager restart/readiness check. Boot recovery runs before the manager and never waits on starting its dependent unit. Ambiguous or failed recovery keeps the journal for repair. No world/DB rewind occurs.

The private readiness handler uses a fresh 256-bit one-use bearer challenge on a loopback-only finite route, distinguishes readiness from SPA/database liveness, hashes `/proc/self/exe` on Linux and checks candidate version. The root controls readiness only after all startup reconciliation/dispatch/observation setup; it revokes readiness before shutdown. Drain receipt preparation refuses running jobs, unresolved saving leases, pending restores/content/modpack/lifecycle/deletion work and file blockers. Receipt publication follows successful DB Close and precedes manager-lock release. Task18's frozen migration00018 counters and independent update/world-risk markers are included in the drain refusal gate; its broader service composition remains separate.

## Native boundaries

Linux: `/usr/local/bin/minecraft-ui` remains root-owned; `ProtectSystem=strict` and manager `ReadWritePaths=/var/lib/minecraft-ui` remain intact. A separate optional root-owned updater receives only `status`/`request <64hex stage>` through sudo. Caller-side status/request are read-only because sudo inherits the manager mount namespace. The request starts the exact independent `minecraft-ui-update@<stage>.service` template. That worker imports the bounded request into `/var/lib/minecraft-ui-update`, re-verifies source/artifact bytes inside protected storage and serializes with a native lock. Recovery uses a separate unit ordered before manager startup. The optional installer bootstraps the installed binary hash and schema capability through a fixed manager-account probe; empty trust stays disabled. TimeoutStopSec 150 accommodates existing HTTP/drain/final-receipt budgets; forced termination cannot produce a valid drain receipt.

Windows: the manager retains RX-only executable access. The separately installed updater is LocalSystem, uses a local-only ACL-restricted named pipe, validates that requests originate from the actual SCM manager process, and imports into protected privileged storage. The fixed demand-start `MinecraftUIUpdateProbe` service uses the same supplied PSCredential as the manager; SCM handles credentials normally. Its executable is copied only from a verified prior/current manager into the fixed probe path. It reports bounded nonce/hash/schema/current-actor results without HTTP/jobs/game/migration work, including after reboot with no live manager token. Custom SCM recovery actions are refused so they cannot race replacement. Installer/probe/pipe/ACL/rename behavior needs actual Windows execution. Windows privileged publication uses a documented administrative `FlushFileBuffers` fence on the fixed local C: NTFS volume, with volume serial/filesystem/binding checks; UNC, reparse, mount and unsupported filesystem layouts fail closed. Unprivileged staging/control directory publication is explicitly nondurable; imported copies and protected journals are volume-fenced before activation. Whole-volume flush can be costly and has no native/power-loss qualification here.

## Evidence and release matrix

Every entry below is unresolved for stable support unless an actual named release run is linked and independently reviewed. Package fixtures and cross-compiles are not substitutes.

| Required behavior | Existing development evidence | Actual release qualification |
|---|---|---|
| Debian x64 systemd, fresh DB → admin → install/start/client → backup/confirmed restore | task04-05 native/process fixtures; shipping UI first-use evidence in feature reports | Pending; no designated installed real host run |
| Ubuntu x64 same workflow | Development Linux build only | Pending |
| Windows 10/11 or Server 2019+ native same workflow | Native cross-compilation; no Windows execution environment | Pending |
| Vanilla/Paper/Fabric/Forge/NeoForge installation/start/update/recovery | Task07/08 exact installer-only smokes; no support inference | Every claimed tuple needs real game/client/platform evidence |
| 1.12 legacy layout | Runtime/loader/world package fixtures; exact installer evidence where reported | Real saved world, client and restore pending |
| 1.16 packing | World codec fixtures (Task21) | Actual version/layout/client/restore pending |
| 1.18 negative Y | World codec fixtures (Task21) | Actual version/layout/client/restore pending |
| 1.20.5 mixed codecs | Codec fixtures; preserve per-chunk codec provenance | Actual mixed-codec world/restore pending |
| 1.21.9 authenticated JSON-RPC MSMP over WebSocket | Native protocol/parser/static evidence where reported | Authenticated real server/client/run pending |
| 26.1 folders | Path/format fixtures | Actual folder-layout world/restore pending |
| 26.3 logs | Task23 official static bytecode/native grammar evidence | Actual log/game/event/client correlation pending |
| Online save lease, watchdog, crash/save-on cleanup | Task13/14 lease/journal deterministic tests | Real online save/timeout/manager-crash evidence pending |
| Explicit restore safety backup and stopped recovery | Task14 deterministic forward recovery tests | Actual platform/client/restore pending |
| Instance version updates, pinned automatic updates, conflicts/competing writes | Task18 pending integration | Actual qualification pending |
| Manager signed hash/archive verification and failed-health rollback | Task24 temporary real-file/journal tests and injected driver outcomes | Installed Linux/Windows update/reboot/rollback pending |
| Roles, stream checks, secrets/audit redaction | Feature API/auth tests; Task24 admin route matrix | Independent whole-branch audit plus actual workflow pending |
| 390 px keyboard first use | Actual fresh-DB Task24 page at390px/desktop, keyboard refresh and real role denials; exact binary below | Stable release/native workflow remains pending |
| Dependency/MIT notices | LICENSE and existing copied notices retained; archives require notices | Complete dependency artifact/notice review pending |

`release/qualification.json` is deliberately disabled with null required evidence. `release/check_qualification.py` refuses an unqualified stable version/source/key/evidence set; removing rows cannot evade its required-key set. The workflow has read-only permissions and no signing, tag trigger or publication capability. An explicit release-owner authorization and independently reviewed signing/publishing workflow remain necessary even after the machine gate passes.

## Verification log

Initial missing-feature test runs failed on missing verifier/stager/source/API/schema/hook declarations before implementation. The native failure tests explicitly drove correction of missing old-binary readiness and boot-recovery unit start ordering: both failed, then passed after fixes.

- `go test -race ./internal/selfupdate ./internal/db ./internal/api ./cmd/minecraft-ui ./cmd/minecraft-ui-update` — PASS at the intermediate integrated checkpoint; subsequent final checks recorded below.
- `cd web && npm run typecheck && npm run lint` — PASS at the same checkpoint.
- Windows selfupdate/main test cross-compilation — PASS; compilation only.
- `sh -n deploy/linux/install.sh` and `git diff --check` — PASS at checkpoint.
- `python3 release/check_qualification.py v1.0.0` — expected refusal: disabled publishing, dev version, missing configured source/key, and all required actual evidence.

Final verification, unresolved integration findings and independent review are appended by this worker/integrator. Task 24 acceptance remains pending.

## Final worker verification and actual UI evidence

The actual embedded shipping binary `/home/thim/.cache/dilla-tmp/minecraft-ui-task17-19` (SHA-256 `6d9afea7e7c4d879f4a74fd868cf5d36115754716a6d8aa10ad5c95224948a62`) served a fresh private data directory at loopback18179. An isolated headless Chromium session created the first administrator through the shipping UI and reached Manager updates through its navigation. At390px the document/main/cards fit the viewport; keyboard Enter refreshed status; screenshots were captured after navigation animations completed. Desktop1365px also rendered. No browser response was mocked and no database fixture seeded onboarding.

Actual API responses: administrator status reported `0.0.0-dev`, schema16, trust false and activation false; check/stage/apply each returned503. Current administrator created viewer/operator accounts through the shipping API; each actual browser login received403 from status/check/stage/apply, and the UI route showed administrator access required. Zero page errors were observed. This validates the disabled development/trust workflow, not configured downloads, privileged activation, a real Minecraft server or stable platform support.

Artifacts: `/home/thim/.cache/dilla-tmp/task24-browser/{manager-updates390.png,manager-updates-desktop.png,results.json}` and reproducible browser scripts in that private cache. The test service was stopped after verification. Temporary evidence data remains private; no user browser profile was touched.

`sh internal/selfupdate/testdata/caller_namespace.sh` PASS: a separate user+mount namespace/chroot made the entire caller filesystem read-only except the fixed service data bind. Actual updater `status` and `request <stage>` succeeded with test-only public trust/bootstrap files. Protected native manager.lock/request.json remained absent; the sole injected OS operation recorded the exact validated systemctl template start. The host systemd/sudo/installation was never invoked. The first fixture attempt omitted `/dev/null` and failed before exec; adding an empty read-only stdin fixture produced the recorded pass.

`sh internal/selfupdate/testdata/insufficient_space.sh` PASS: a private1MiB tmpfs caused actual `ENOSPC` while staging a valid signed archive with2MiB random executable data. Staging returned the underlying error and removed partial publication. No host disk was filled. The opt-in package test explicitly skips in ordinary runs; its separate namespaced execution supplies this evidence.

Native tests inject nine distinct durability fence failures, failed candidate health, failed stop, revoked administrator, refused/incomplete drain, schema mismatch, conflicting executable scratch and interrupted replacement. They use actual temporary files/journals with injected service/flush outcomes. No injected outcome qualifies systemd/SCM/NTFS or physical power-loss behavior. In particular, old-executable readiness, safe boot ordering, safe post-stop failure recovery and late receipt-fence failure rollback were each observed failing before their fixes.

Root-owned main/App/navigation composition is reviewed separately. Task18's frozen migration00018 drain contract was integrated: nonterminal update_transactions and backup_update_rollbacks plus any update-transaction.json/update-world-risk.json marker refuse a receipt. Tests cover ordinary update states, every active rollback successor state, terminal validated supersession and malformed orphan markers. This narrow drain dependency does not claim Task18 service/update acceptance. Stable publishing and every required native/game/client release row remain blocked.

Final worker check after frozen supersession integration: `go test -race ./internal/selfupdate ./internal/update ./internal/db ./internal/api ./cmd/minecraft-ui ./cmd/minecraft-ui-update` PASS (selfupdate2.304s, update2.880s, db2.441s, API7.674s, main4.587s); corresponding scoped `go vet` PASS. Windows updater cross-build and Windows main/selfupdate test cross-compiles PASS; these binaries were not executed on Windows. Frontend typecheck/lint passed after the final UI/schema composition and no frontend changes followed. Final read-only-caller namespace and real tmpfs ENOSPC fixtures both PASS; installer shell syntax and `git diff --check` PASS. No host PowerShell/systemd/SCM/native updater activation or physical durability test ran.

The frozen Task18 validator is called inside the original read-only drain transaction, with no nested connection/admission. Terminal `superseded` parents are allowed only after `update.ValidateSupersession` validates reciprocal one-hop links, exact immutable backup consent and ready full target/new protected safety records. Own integration regressions reject active children, orphan terminal links and invalid terminal consent; validator-specific tests cover remaining malformed topologies. Migration00018 is a required shared dependency, independently owned/reviewed by the integrator.

Windows flush design source: [Microsoft FlushFileBuffers documentation](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-flushfilebuffers) explicitly describes administrative volume-handle flushing. Unprivileged directory publication is not represented as a Windows durability guarantee. Release ZIP directory preflight (three entries,16KiB maximum, no ZIP64/comment/trailing payload) runs before `archive/zip` can allocate central entries; this addresses the standard parser's modulo65536 count/read-until-signature behavior inspected in the installed Go1.27 source.

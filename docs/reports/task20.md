# Task 20: JVM metrics and notifications

2026-10-16. Implementation and delegated composition submitted for integrator review. The integrator owns acceptance and ledger updates. No files were staged or committed by this worker; no vault writes were performed.

## Implemented interfaces and composition

- `metrics.New(metrics.Config{DB, Paths, Instances, Native, Players})`, `Run(ctx) error`, `Query(ctx, metrics.Query) (metrics.Page, error)`. `api.MountMetrics` mounts a finite, authenticated viewer read at `/instances/{instanceId}/metrics`.
- `notify.New(notify.Config{DB})`, `Run(ctx) error`, private typed `Enqueue`, masked administrator configuration and bounded history. `api.MountNotifications` mounts administrator-only `/notifications/channels`, channel PUT/DELETE and `/notifications/history`.
- Migrations `00011_metrics.sql` and `00012_notifications.sql` own persistence. Root merged the frozen OpenAPI fragments and generated types in `005a009`; generated types were not edited by this worker.
- Root explicitly delegated new launcher metric files, `launcher/inspection.go`, new `supervisor/metrics.go`, and final `main.go`, App, Shell and instance-link composition. Existing startup recovery ordering is preserved. `watchObservability` terminates HTTP on unexpected loop exit. Both loops cancel and drain before the existing queue/native/database shutdown sequence.
- Shipping UI exposes JVM metrics from server detail and notifications in administrator navigation. All feature DTOs come from generated API types. Metrics include a keyboard-scrollable bounded history viewport; notification forms provide explicit replace/clear controls and visible outcomes.

## JVM and instance metrics

Two bounded workers sample on the 30-second ticker with per-instance pending admission and round-robin overload handling. Native reads have a four-second collection budget; a disk scan has a separate two-second budget, 20,000-entry and 32-level limits, and batched directory reads. Missing or over-budget fields remain null. Database failures are visible manager failures rather than silently stopped observation loops. A deleted instance racing an admitted sample is checked transactionally and produces no ghost row or fatal foreign-key error.

The exact captured JVM identity is checked before and after native sampling. Linux uses the existing fixed `inspect <validated-instance-ID>` broker, its derived direct JVM child, pidfd/cgroup/namespace/unit-generation protections and strict broker-result validation. No new privileged verb, caller-selected PID, arbitrary filesystem root, shell or namespace entry was introduced. Optional counter failure does not invalidate identity-only inspection.

Linux collects bounded `/proc/<derived-JVM>/stat` CPU ticks and resident pages, bracketed by the process start identity. Linux x64 USER_HZ is 100; CPU includes user plus kernel time for the process, excluding waited-for children. Windows uses a real process handle, creation FILETIME, boot GUID, image identity, GetProcessTimes and GetProcessMemoryInfo. Working set and committed private bytes are distinguished; unavailable memory remains null. Private bytes are unavailable on Linux. Neither platform invents Java heap values or launcher metrics.

CPU uses cumulative process CPU delta divided by elapsed monotonic time: 100% is one fully busy logical CPU and multithreaded JVMs may exceed 100%. The first observation, changed generation, counter regression, excessive interval or detected clock discontinuity has no CPU percentage. Manager restart starts a fresh CPU baseline. Instance disk is logical file bytes inside the validated instance root, including server/logs/backups/state, excluding shared runtimes and `manager.db`; symlinks/special files and partial scans are rejected. It is not host free-space telemetry or a coherent filesystem snapshot.

Player counts consume Task 11's typed snapshot and require a currently verified JVM, matching PID/start generation, known complete roster, and fresh observation. Unknown presence remains null; a verified zero-player roster is a measured zero. Source fields retain typed provenance, observation age and safe gap reasons. Player session/history UI links to the existing Task 11 feature.

Raw buckets retain seven days, hourly buckets 90 days. Atomic, idempotent per-instance/bucket insertion updates hourly sums and individual field counts; missing fields do not enter averages. Hourly expected coverage is 120 observations, including an honestly incomplete current hour. Queries synthesize absent buckets as gaps and validate retention, range, resolution and a 1000-point maximum. The 90-day UI uses multiple bounded requests. Histories expose no PID, private handles, launch argv/environment or host paths.

## Notifications, privacy and durability

Discord, Slack, ntfy, Telegram, generic webhook and email adapters are implemented. The Valheim notification adapters/rules and its MIT license were inspected as the approved behavior reference; this package is a separately authored, narrower implementation. Primary documentation checked during implementation:

- [Discord execute webhook](https://docs.discord.com/developers/resources/webhook#execute-webhook): `wait=true` and an empty allowed-mentions parse list.
- [Slack incoming webhooks](https://docs.slack.dev/messaging/sending-messages-using-incoming-webhooks/): plain-text blocks, disabled markdown/link-name expansion, bounded `ok` response validation.
- [ntfy publishing](https://docs.ntfy.sh/publish/): plain body, bounded fixed headers and optional bearer authentication.
- [Telegram Bot API](https://core.telegram.org/bots/api#sendmessage): explicit chat ID, no parse mode, disabled previews and bounded `ok` validation.
- [Linux process stat](https://www.man7.org/linux/man-pages/man5/proc_pid_stat.5.html), [Windows process memory counters](https://learn.microsoft.com/en-us/windows/win32/api/psapi/ns-psapi-process_memory_counters_ex).

New channels must first be saved disabled, then explicitly enabled on a later administrator save. Up to 24 channels select `job_failed` and optionally `job_succeeded`. Destinations, URL/query/path tokens, bearer secrets, SMTP username/password and recipient addresses remain private manager-database fields and never enter a JVM environment, generic settings projection, public response, transport error, job input or delivery-history row. Public configuration returns only identifier/label/provider/event choices, enable/private-network choices and masked presence booleans. Replace supplies a complete destination; omitted replacement retains it; clear erases it and disables delivery. Configuration writes/deletes are audited atomically without credential-bearing details.

Only finite typed job event data is templated: instance ID, an allowlisted operation and terminal outcome. No raw job failure, console/log content, player name, administrator label, message code or user-defined template enters a payload. Discord mentions are disabled, Slack blocks are plain text, Telegram has no markup parser, and mail headers/addresses reject injection.

HTTP endpoints require HTTPS and provider-specific host/path validation. Proxy environment variables are ignored; redirects are rejected; bounded DNS answers are validated and pinned at connect time. Private/loopback destinations require explicit administrator host-scope selection; link-local metadata, unspecified and multicast addresses remain forbidden. SMTP requires certificate-verified TLS on connection or mandatory STARTTLS before authentication; absent STARTTLS cannot downgrade credentials to plaintext.

The durable SQLite terminal-job outbox captures actual transitions for `backup_create`, `backup_restore`, `lifecycle`, `content_mutation`, `modpack_apply` and `update_apply`; ordinary commands and readiness verification are excluded. No historical terminal jobs are copied at migration. The event ID deduplicates job ID plus terminal state, so an older job finishing late is retained. An explicit AUTOINCREMENT event sequence and per-channel transactionally captured configuration baseline prevent pre-enable events from crossing the enable boundary, even within the same clock second. Sequence retention tests prove that emptying the outbox cannot reuse a suppressed sequence.

Delivery is one worker with at least one second between attempts, ten-second transport deadlines, three total attempts, and 30-second/2-minute retry delays. The active queue is capped at 1000; excess delivery admission records safe `queue_full` failure. Sent, failed, queued and in-flight are durable; a crashed/canceled in-flight attempt becomes `unknown_after_crash` and is not automatically replayed. Retries of ambiguous transport failures can duplicate delivery; exactly-once delivery is not claimed. Configuration changes cancel queued work so old events cannot silently reach replacement recipients. Terminal delivery history retains 30 days/up to 10,000 rows, with pending expiry and bounded outbox processing. Only finite safe failure codes are exposed; raw HTTP/SMTP errors, bodies, endpoint URLs and recipients are discarded.

Task 19 remains the canonical producer of proven crash notifications. No running-to-stopped transition is misclassified as a crash by this package. The reviewed typed producer boundary remains separate from Task 19 policy.

## Verification evidence

Passed focused commands on the integrated working tree:

```sh
go test ./internal/metrics ./internal/notify ./internal/launcher ./internal/supervisor ./internal/api ./cmd/minecraft-ui -count=1 -timeout=90s
go test -race ./internal/metrics ./internal/notify ./internal/launcher ./internal/supervisor ./internal/api ./cmd/minecraft-ui -count=1 -timeout=120s
go vet ./internal/metrics ./internal/notify ./internal/launcher ./internal/supervisor ./internal/api ./cmd/minecraft-ui
cd web && npm run build && npm run lint
env GOOS=windows GOARCH=amd64 go build -o /home/thim/.cache/dilla-tmp/task20-minecraft-ui-windows.exe ./cmd/minecraft-ui
```

After final provenance/deletion/outbox-baseline changes, focused metrics/notification/API/main tests passed again; metrics/notify race tests and scoped vet passed. The last outbox-empty-retention regression also passed. No broad `go test ./...` was run during parallel work.

Meaningful deterministic/local tests cover:

- First CPU sample, 50% CPU delta, recycled JVM generation, verified zero players versus unknown/stale generation, absent observation buckets, exact raw/hourly retention boundaries, 120-point hourly coverage, idempotence and missing-field counts/means.
- Disk symlink rejection, cancellation, strict query bounds, and concurrent instance deletion admission.
- Linux stat parsing, overflow/zombie/malformed rejection, actual current-process counter reads and rejected wrong start identity/fixed-broker bypass. The current-process check is a local OS primitive check, not Minecraft qualification.
- Controlled TLS receivers for all five HTTP payload formats; redirect, auth, rate-limit, malformed/oversized response, untrusted TLS and timeout failures; controlled authenticated TLS SMTP success and mandatory-STARTTLS downgrade refusal.
- Masked private configuration, role enforcement, safe audit/history, duplicate outbox processing, late older job completion, exact retry backoff/attempt count, unknown after crash, disable/replace/clear cancellation, queue limit, retention boundary and transport shutdown drain.
- A real `jobs.Service` terminal transition reaches a controlled webhook receiver, records durable `sent`, and never forwards the feature's private failure text. Only that job's feature handler is a deterministic failure fixture; its state is changed by the real queue.
- Authenticated finite API role matrix, read versus admin PAT scope, revocation and disabled-account reauthentication. New main-loop tests verify unexpected failure shuts HTTP with safe output and cancellation waits for worker drain.

A timeout receiver fixture initially waited indefinitely during `httptest.Close`; the fixture was corrected to release its controlled handler. All receiver tests now complete locally. One assertion initially compared `domain.Error.Error()` without its code prefix; it now checks the typed public message. Neither issue was hidden by removing a behavioral assertion.

## Shipping browser evidence

The Browser skill was read; runtime bootstrap succeeded but browser selection reported no available browser and discovery returned `[]`. Root had explicitly authorized an isolated standalone Playwright context for this case. No user browser profile/session was used.

A fresh local embedded manager on `127.0.0.1:18170`, Chromium at 390×844, exercised first-admin setup, empty Servers, official Vanilla 1.21.9 selection, shipping server creation with EULA unchecked, server metrics navigation and keyboard refresh. The actual metrics endpoint returned 200 with null CPU/RSS/players when no verified JVM existed. Periodic collection later recorded positive instance disk bytes while retaining unknown JVM values, including after a manager restart. The 90-day control loaded without API error via bounded requests.

Notification UI saved a disabled generic webhook destination, replaced its private URL/token, checked actual PUT/GET masked responses, and cleared it. No test channel was enabled. The final database has the test destination disabled and cleared; delivery history is truthfully empty. Viewer and operator users were created through the shipping Users UI; both read metrics (200), received 403 for private configuration/history, had no notification navigation link, and were denied the direct administrator route. Keyboard save/refresh and scrollable history were exercised. No page errors or horizontal document overflow occurred.

Artifacts in `/home/thim/.cache/dilla-tmp/task20-browser/`:

- `evidence.json`, including final-build periodic sample summaries, authorization results and exact masked response keys.
- `metrics-final390.png`: refreshed screenshot after bounding the history viewport; visually inspected.
- `notifications-final390.png`, plus disabled/cleared and viewer/operator screenshots.

No real provider accounts, people or external channels received messages. Local TLS/SMTP receivers are the only delivery targets used by tests. No Minecraft JVM was launched and no host unit was installed.

## Remaining qualification

Real installed Linux systemd JVM/broker sampling, live Minecraft/client player correlation, supported distribution/version combinations, and native Windows execution still require qualification. Windows cross-compilation is build evidence only. External production-provider delivery needs separate user authorization and configured credentials; it is not implied by local adapter fixtures. Task 19 crash-event binding remains with its owner. The implementation report does not mark Task 20 or any release matrix row complete.

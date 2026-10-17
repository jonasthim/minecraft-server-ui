# Task32B hosted Windows updater qualification evidence

Run `39044758885`, candidate commit `d749c2a4234107256382290a16edcd82d915f61f`,
job `117431582136` (`Exercise updater on windows-2022`) completed successfully
on the GitHub-hosted Windows Server 2022 runner. The workflow's overall run
failed because its separate Ubuntu 24.04 exercise failed; the Windows job and
its scrubbed artifact succeeded.

The retained `installed-updater-windows-2022-...` artifact records all three
installed activation cases:

| Case | Recorded outcome | Assertion |
| --- | --- | --- |
| Genuine candidate | `healthy`, version `v0.0.2`, schema 20 | Running manager image hash matches the candidate; process binding and admin session data are preserved. |
| Negative health | `rolled_back`, current version remains `v0.0.2` | Candidate hash differs from restored running image; rollback was attempted and the prior image is bound to the running process. |
| Interrupted candidate | `rolled_back`, current version remains `v0.0.2` | Interruption artifact records the candidate worker/image before recovery; rollback had not happened before recovery, then restored the prior image. |

`api-assertions.txt` records PASS for each case and for the final no-orphan
manager stop. The activation journal summary retains the terminal `rolled_back`
phase and the exact prior/candidate identities. The SCM summary shows the
manager and probe stopped with zero exit codes and the updater service running.
The process-access summary confirms the probe's limited query access and denial
of terminate, memory modification/read, thread creation, DACL, owner, and
duplicate-handle rights. The permission summary records protected
Administrators-owned state and installation directories/files. Scrubbed Event
Log capture succeeded and reported no activation or probe failure events for
this run.

The successful Windows job provides native Windows activation, negative-health
rollback, interrupted-candidate recovery, process-rights, SCM, permissions,
and cleanup evidence for this candidate. It is not evidence for the overall
workflow, Linux, the Minecraft server lifecycle, or installer lifecycle.

Artifacts were downloaded to the private cache from run `39044758885`; no
production files or host services were changed by this inspection.

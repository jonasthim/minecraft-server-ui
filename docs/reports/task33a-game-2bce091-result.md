# Task33A game acceptance result at `2bce091`

Run `39045064246` exercised Minecraft Java 1.21.9 on hosted Ubuntu 24.04
(job `117432040762`) and Windows Server 2022 (job `117432040513`). Both jobs
failed in the `deliberate_stop_restore` phase after earlier steps had installed
the manager, started the real server, completed gameplay marker observations,
and captured/verified the online backup. Neither job reached restore or the
later restart and deliberate-no-restart checks.

## Linux evidence

The scrubbed game summary reports a failed job in `stopping_process` with the
fixed diagnostic `native_stop_unclean`. The independent post-failure status
reports the process stopped. The retained launcher evidence reports matching
identity, an exit record and timestamp, deliberate operator stop, `forced` is
false, exit code 0, and no watchdog diagnostic. This differs from the earlier
Linux `39039284671` summary at `a37cf153`, where the same phase ended with only
`unclassified` diagnostics and the process was also already stopped.

The new artifact narrows the failure: the JVM did complete as a clean deliberate
stop, so the failure is in the stop operation's returned result or its
post-stop bookkeeping, rather than an observed forced JVM termination or a
reported launcher watchdog error. The artifact does not retain which internal
`Supervisor.Stop` substage returned the error. The workflow log retains only
the phase and fixed failure code. Therefore the exact Linux error source is
still unproven.

Source inspection identifies one specific race worth instrumenting: the
launcher `/stop` handler persists the stop intent and signals its stop channel
before writing the HTTP success response. The caller treats any failed
`launcher.Send` response as a failed native stop, even if the launcher then
finishes a clean stop. This ordering could explain a clean stopped process
alongside a failed `stopping_process` job. The hosted artifacts do not prove
that the response race happened in this run. A bounded fixed enum for stop
substage (control request/ack, wait/admission, exit metadata validation) is the
narrow next diagnostic, followed by another hosted run.

## Windows evidence

The Windows job failed at the same phase with `native_stop_unclean`. Its
post-failure process status is stopped, but the launcher diagnostic is
unavailable with `identity_malformed`; the hosted report does not expose the
actual native stop error.

The qualification parser explains why its launcher details are unavailable.
The native Windows `start_identity` in the same report has the shape
`{GUID}:<16 hex digits>`, while `game_api.py`'s `PROCESS_START_ID` regex allows
only an unbraced GUID followed by a colon and hex digits. It rejects the
Windows identity before inspecting the stop/exit records. This is a concrete
diagnostic-fixture incompatibility, not evidence that the Windows process
identity itself is malformed. Extend the anchored parser to accept the exact
braced Windows GUID form and keep the existing Linux form. The stop failure
itself remains unexplained until that rerun yields launcher evidence.

## Scope of evidence

Both artifacts identify candidate commit `2bce091c738ddffb21cb2bce6904de0d3a748c0f`.
The Linux and Windows scrubbed artifacts and the Linux job log were retained in
the private cache at `/home/thim/.cache/dilla-tmp/task30-native-evidence-39045064246`
and `/home/thim/.cache/dilla-tmp/task30-native-evidence-39045064246-windows`.
No game harness or production code was changed during this read-only
investigation. This failed run is not game qualification evidence.

# Task33B legacy online-backup failure investigation

## Retained run evidence

Run `39052369339` at commit `329622eb04201b4f56fbed9fb506adde02ebb4a0` failed
the legacy online-backup step on both hosted tuples:

- Ubuntu 24.04, Vanilla 1.12.2, Temurin 8: `/home/thim/.cache/dilla-tmp/task33b-game-39052369339/installed-game-ubuntu-24.04-1.12.2-329622eb04201b4f56fbed9fb506adde02ebb4a0/game-summary.json`
- Windows Server 2022, Vanilla 1.12.2, Temurin 8: `/home/thim/.cache/dilla-tmp/task33b-game-39052369339/installed-game-windows-2022-1.12.2-329622eb04201b4f56fbed9fb506adde02ebb4a0/game-summary.json`

Both summaries report `online_backup`, terminal job failure at
`verifying_backup`, and only `unclassified` job and console reason codes. At
the later diagnostic read, both managers report the process running, control
connected, game ready, required settings verified, and ready for use. Each
host connected twice with the locked 1.12.2 protocol client; both sessions saw
49 chunks, a teleport, and a keepalive, and the second session observed yaw
31.25. The Ubuntu manager identity remained unchanged through those sessions.

The Windows `result.json` records `last_stage=game_api`, exit failure, and
`failure_diagnostic.source_line=101`. That line is the wrapper's intentional
`throw 'game_acceptance_failed'` after `game_api.py` exits nonzero; it does not
identify a Go backup source line or error cause. The retained artifact set has
no raw job error, job log message, server log, or save-lease rows. No raw error
was inferred from the `unclassified` code.

## Source boundary and hypothesis

`runCreate` stores phase `verifying_backup` at 5% before decoding the request or
calling `CreateForJob` ([service.go](../../internal/backup/service.go#L179)).
The online path first verifies native identity/process state, opens a scoped
console job session, rechecks its identity and current requester, and only then
calls `capture` ([service.go](../../internal/backup/service.go#L205)). Before
`capture` records `preparing_backup` at 10%, it checks outstanding leases,
backup readiness, instance paths and metadata, then calls `worlds.Discover`
and validates the discovered layout ([service.go](../../internal/backup/service.go#L327)).

The hypothesis supported by the phase boundary is that the failure occurred in
one of those pre-capture admission or preparation gates. It is not evidence of
a failed save-off/flush, world copy, ZIP creation, archive validation, or
publication: those steps occur only after `preparing_backup` and later phases.
The phase is too coarse to distinguish native identity/inspection, console
session admission, lease/readiness guards, instance reads, or world discovery.

World layout alone is not an established cause. `versionLayout` accepts
Vanilla 1.12.2 as `legacy` ([service.go](../../internal/world/service.go#L117)),
and `TestDiscoverActualVersionedLayouts` exercises a synthetic 1.12.2 legacy
tree ([discovery_test.go](../../internal/world/discovery_test.go#L27)). That
fixture does not reproduce the hosted world tree or prove hosted discovery
succeeded.

## Next evidence needed

The current packet makes only a harness classification change. It maps exact
existing authored backup messages to finite `backup_*` codes and recognizes
the exact finite Linux process identity stage set (`proc_stat`,
`proc_executable`, `proc_working_directory`, `broker_path`, `broker_invoke`,
`broker_decode`, `broker_identity`, `process_metadata`, and the existing
`boot_identity`). Unknown stages, suffixes, and all other messages remain
`unclassified`; no log text, path, native output, or SQL detail is returned.
The classifier does not add stage detail around the backup gates, so the
retained run remains unclassified for its actual cause. No source correction
is justified by those artifacts. This packet makes no claim about the exact
failing gate or backup correctness on either host.

Verification: `PATH=/home/thim/.cache/dilla-tmp/task33a-powershell-7.6.7/runtime:$PATH PYTHONPATH=deploy/qualification python3 -m unittest deploy.qualification.game_api_test`
passed all 34 fixtures, including exact and adversarial `job_diagnostics`
messages.

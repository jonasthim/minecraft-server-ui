# Task 30a: PowerShell 5.1 argv fixture compatibility

## Change

The Windows behavior harness now parses the captured native argv JSON using a
plain `ConvertFrom-Json` assignment, then checks that the result is an array,
has the exact expected number of entries, and contains the exact expected
string at every index. This avoids the old `@(ConvertFrom-Json ...)` capture,
which can wrap a JSON array emitted as one pipeline object in another array.
The production command arguments are compared in full, including the
repository, private trust path, version, output path, and optional development
flag.

The harness includes a PowerShell 5.1 semantics fixture. It uses
`Write-Output -NoEnumerate` to model the historical single-object array
emission on the current engine, verifies that the old indexed assertions fail
against that shape, and verifies that the new guarded assertion accepts both
normal and legacy-shaped decoding. Malformed JSON, a truncated array, and a
changed argument are rejected in both fixture modes.

## Failure evidence and scope

Hosted run [39041685406](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39041685406),
job `117422859504`, tested commit `71b0c66452066f0b5b23416631581dd791edc0e2`.
Its retained log reports that the Windows PowerShell 5.1 harness failed with
`Index was outside the bounds of the array`; the PowerShell 7 harness and
native preparer passed. The log does not retain an inner script line or stack,
so attributing that failure to the argv fixture remains an inference.

The fixture is supported by the PowerShell project's description of the
Windows PowerShell behavior and subsequent compatibility change:
[PowerShell issue #3424](https://github.com/PowerShell/PowerShell/issues/3424)
documents JSON arrays being emitted as a single object, and
[PowerShell issue #15280](https://github.com/PowerShell/PowerShell/issues/15280)
documents PowerShell 7's changed array enumeration behavior and the
`-NoEnumerate` compatibility option. The inspected shipping template does not
parse JSON arrays; the failing code was in the harness's argv assertion.

## Verification

- Extracted and ran the harness's argv fixture block with PowerShell 7.6.6 —
  this initial check omitted the harness's top-level strict mode, so it was
  insufficient to validate the negative control.
- Re-ran the exact fixture prefix, including `Set-StrictMode -Version Latest`
  and `$ErrorActionPreference = 'Stop'`, with PowerShell 7.6.7 — passed. The
  legacy indexed assertion now catches the expected out-of-range exception;
  all new decoder checks and refusal cases then pass.
- Parsed the complete `windows_behavior_test.ps1` with the PowerShell 7.6.6
  parser — passed.
- The fixture executable was kept under `/home/thim/.cache/dilla-tmp`; it is
  not a repository dependency.

The updated full harness has not yet run on Windows PowerShell 5.1. A hosted
Windows rerun is required to confirm the original failure is fixed on that
engine.

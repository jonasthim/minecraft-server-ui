# Task30A PowerShell 5.1 scratch cleanup failure

## Observed result

Hosted candidate run `39046958480` at `06221bb`, Windows job `117438077444`, passed the PowerShell 7 behavioral harness. The PowerShell 5.1 harness then printed four expected scratch-child ACL checks for the `good` case and failed at its post-child assertion `Test-Path -LiteralPath $scratch`, reporting `owned scratch leaked`. The bounded retained log is `/home/thim/.cache/dilla-tmp/candidate-39046958480-windows.log`.

The assertion did not include the current mode. The four ACL lines were printed by the `good` case, which reached native-argument verification, created and inspected the expected owned files, and produced the expected child exit result. They do not establish that the later leak assertion belonged to `good`; a subsequent negative case could have left a scratch path behind. The run therefore shows that one case left a scratch path behind, but does not identify which case or cleanup operation failed. The PowerShell 7 pass does not isolate the cause to a particular engine behavior.

## Diagnosis limits

The shipping template's `finally` block catches cleanup exceptions, emits only `private bootstrap scratch cleanup refused`, and sets a boolean. Before this packet, the temporary harness instrumentation recorded stage/type/line/HRESULT only for uncaught top-level errors. It did not capture cleanup's swallowed error, the path's post-cleanup state, or its remaining children. The retained log contains no fixed cleanup warning, identity, or residual-entry metadata from which to distinguish a failed file disposition, a failed directory disposition, or an independently remaining entry.

I therefore cannot claim a concrete product cause from this run. The successful ACL observations do not justify weakening or skipping the owned-identity checks.

## Narrow next step

The diagnostic-only PowerShell 5.1 workflow step now instruments the temporary copy of the shipping source. It records fixed cleanup stages (`close_handles`, `verify_scratch`, one of four fixed child names, or `delete_directory`), allowlisted exception-type labels for the outer exception and up to three inner exceptions, and bounded line/HRESULT/Win32 codes. The parent diagnostic reports a validated fixed case and reason label on any unexpected child exit or scratch leak, plus bounded scratch/ACL booleans and a capped count with booleans for the four expected entries and unknown entries. It checks only for the two authored cleanup-refusal strings. It does not emit paths, residual names, raw exceptions, arguments, or ACL text. The cleanup logic and assertions remain unchanged.

The generated temporary harness and instrumented entry source parse under portable PowerShell 7.6.7. A synthetic residual-directory fixture confirms the fixed mode/reason/stage labels, count and entry booleans, bounded inner exception labels and numeric codes, and omission of supplied path and entry-name strings. These checks validate the diagnostic packet's structure only; they do not reproduce Windows PowerShell 5.1 cleanup behavior. The generated fixtures are under `/home/thim/.cache/dilla-tmp/task30a-ps51-diagnostic-step.ps1`, `/home/thim/.cache/dilla-tmp/task30a-expanded-harness.ps1`, `/home/thim/.cache/dilla-tmp/task30a-instrumented-entry.ps1`, and `/home/thim/.cache/dilla-tmp/task30a-diagnostic-fixture.ps1` (output: `/home/thim/.cache/dilla-tmp/task30a-diagnostic-fixture.log`). The next hosted Windows run must identify the case and cleanup stage before any shipping-template change is considered.

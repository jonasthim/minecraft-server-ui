# Task30A PowerShell 5.1 good-case failure

## Hosted evidence

Candidate run `39045403927` at `b58749419cdadaa7402a424981f5ff2a770b4726`, Windows job `117433836255`, completed native preparer tests and the PowerShell 7.6 behavior gate. The PowerShell 7 gate printed all four expected scratch ACL checks and its full behavior suite passed.

The following Windows PowerShell 5.1 step reached `windows_behavior_test.ps1` and failed at its first `good` case with `unexpected result for good`. That assertion is the child `powershell.exe` process exit check: the test expects zero but received a nonzero status. The workflow log contains no `Atomic scratch ACL verified` lines from this step, so it failed before the parent harness completed argv and ACL assertions.

The harness captured child output in `$captured` but did not print it when the exit assertion failed. The retained log therefore cannot establish whether this is a generated entry-script refusal, a test-fixture/runtime issue, or a failure after the fake runner. No evidence justifies changing the shipping PowerShell template yet.

## Workflow diagnostic

The PowerShell 5.1 workflow step now creates a temporary copy of the existing behavior harness and instruments only that temporary fixture. The generated entry script receives markers at `Initialize-PrivateIO` and `New-PrivateScratchFile`, and its authored outer catch writes a bounded diagnostic JSON record to a per-case file. The record contains a fixed stage enum (`entry_start`, `initialize_private_io`, or `new_private_scratch_file`), an exception type from a finite allowlist, a script line bounded to `0..5000`, an eight digit hexadecimal HRESULT, and a Win32 code bounded to `0..65535` (`-1` when the actual exception is not a `Win32Exception`). The parent reports only validated fields, plus a bounded child exit code (`0..255`, otherwise `-1`) and fixed argv status (`match`, `mismatch`, or `unavailable`). It never writes captured child output, paths, argv contents, exception text, or stack traces. The shipping template and its authored outer error remain unchanged; all existing assertions and guard cases remain active.

On the next hosted Windows PowerShell 5.1 run, these fields should locate a failure in the early scratch setup and identify whether execution reached the fake preparer runner with matching argv. The gate still fails on the unexpected child result. The source cause remains unclassified until that native run supplies the fixed diagnostic fields.

## Verification and limits

- Parsed the edited workflow YAML successfully.
- Parsed the workflow step and generated temporary harness with the portable PowerShell 7.6.7 runtime.
- Parsed an instrumented child-source fixture and triggered its authored catch; checked that the recorded stage, exception type, bounded line, HRESULT, and Win32 code satisfy the fixed output schema.
- `git diff --check -- .github/workflows/candidate-packages.yml` passed.

No shipping bootstrap/outer source changed. No native PowerShell 5.1 run or workflow dispatch was performed locally; that runtime is available only on GitHub Actions. This report does not claim the underlying PowerShell 5.1 failure is fixed.

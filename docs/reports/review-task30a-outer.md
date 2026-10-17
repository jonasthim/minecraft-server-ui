# Independent review: Task30A2 outer preparer entry scripts

Reviewed `9969bb7..2739f92` against the frozen Task30A2 brief, approved design,
original and corrective plans, acquisition ruling, candidate contract, and
implementation ledger. The exact frozen review diff has SHA256
`bedadaad4d7f0a6bf0718275c18c1a651c4307fca573db209194e1908431e37c`.
The nine committed files are the outer producer, command, templates, tests,
Windows behavior harness, candidate snapshot adapter/tests, and worker report.
The independently changing workflow/scripts and other workers' files are outside
this review. The reviewed implementation files still match `2739f92`.

## Verdict and findings

Specification: **PASS for this prepare-only source packet**. Security/quality:
**APPROVE**. Zero Critical, Important, or Minor findings established.

This accepts source for integrator composition and hosted execution. It does not
complete Task30, authenticate a public distribution channel, qualify Windows,
or accept installer lifecycle or stable publication.

## Reviewed boundaries

- The producer validates exact stable preparer identity and repository, parses
  bounded independently supplied public trust, and snapshots both binaries before
  publication. `SnapshotNativeInput` restricts commands/platforms/schema and
  directly reuses the existing snapshot, executable/build-information and strict
  receipt checks. Unsigned receipts are correctly described as provenance rather
  than proof of native execution. Existing candidate formats and allowlists are
  unchanged.
- Output has exactly the five contracted files and deterministic public
  provenance. Existing outputs refuse before publication; exclusive output
  creation and retained identity checks preserve replacements on failed cleanup.
  The private-parent precondition and successful-exit consumption rule are
  explicit; no atomic directory-publication claim is made.
- Both entry scripts pin the preparer release URL independently of the selected
  manager version, embed the reviewed public trust and exact binary size/hash,
  and place both checks before native invocation. They only invoke the preparer;
  they do not invoke extracted installers. Development mode requires explicit
  consent and owner mode refuses the development switch.
- Linux uses literal array arguments, restrictive umask, a fresh `/tmp` child,
  exit/signal cleanup, fixed HTTPS acquisition, disabled curl configuration and
  proxies, finite transfer/redirect bounds, and a bounded body sink. Only the
  exact HTTPS GitHub release-assets host passes redirect validation. Raw download
  errors and signed redirect queries are kept out of public output.
- Windows uses explicit protected directory security and atomic CREATE_NEW file
  security. Elevated directory/files have Administrators ownership and only
  Administrators/SYSTEM access; unelevated operation additionally permits the
  current identity. The Win32 adapter checks handle-bound IDs, owners, complete
  DACL rules, reparse attributes and single-link files. Trust and the verified
  image retain handles without write/delete sharing through invocation.
- Windows cleanup checks the recorded ID/security on the exact DELETE handle;
  only the four successfully created fixed filenames are eligible. Unknown
  children and substituted/changed identities cause refusal. Compiler startup
  follows private directory creation and ACL validation, with TEMP/TMP redirected
  during Add-Type and restored in finally. The report accurately retains native
  compiler behavior, file-sharing and cleanup-disposition observation as gates.

## Independent verification

Commands executed locally:

```sh
git diff 9969bb7 2739f92 --check
go test -race -count=1 ./release/outer ./release/cmd/outer ./release/candidate
go test -race -count=1 ./release/candidate -run TestNativeSnapshot
GOFLAGS=-buildvcs=false go test -race -count=1 ./release/candidate
go test -race -count=1 ./release/outer -run TestLinuxEntryBehavior -v
go vet ./release/outer ./release/cmd/outer ./release/candidate
```

Diff check and vet passed. Outer tests passed, including producer snapshots,
strict receipt refusals, reproducibility, existing-output preservation and all
11 Bash behavior cases. The command package has no tests. Focused adapter tests
passed. The first broad candidate run failed in the unchanged
`TestCandidateContract` fixture build with `error obtaining VCS status: exit status
128`; rerunning that package with only VCS stamping disabled through GOFLAGS
passed under race in 23.870s. This environment-related first failure is not
reported as a passing unmodified broad command.

A separate temporary Go overlay changed only the test renderer to remove the
rendered SHA256 refusal. Running
`go test -race -count=1 -overlay=/tmp/task30a-outer-review-e33xc_ug/overlay.json ./release/outer -run 'TestLinuxEntryBehavior/tampered_never_runs'`
then failed because the same-size tampered payload reached the fake runner and
returned success. The unmodified behavior suite subsequently passed in 1.104s.
This independently demonstrates that the tamper test detects loss of the real
hash barrier. No reviewed implementation file was edited for this probe, and no
downloaded fixture executable was run.

## Remaining evidence limits

PowerShell is unavailable locally. The generated Windows script and supplied
native behavior harness were inspected, not executed. C# source compilation
reported by the worker is not native API qualification. The hosted gate must
still exercise actual PowerShell versions, atomic child owner/DACL observations,
compiler temporary files, retained-image sharing, output redirection and cleanup.
The harness replaces final launch with a test runner, so its argv assertion
observes the argument array before native process marshaling. Actual preparer
invocation and preparation remain separate integration evidence.

Linux behavior uses the disclosed test downloader, os-release fixture and final
runner seam; it proves source refusal/control flow rather than public GitHub
acquisition or Debian installation. Native build receipts, actual pinned/latest
acquisition, independently authenticated outer distribution, privilege-crossing
verification and installer lifecycle remain with the integrator. No services,
accounts, firewall, game state, preview, product implementation, staging or
commits were changed during this review.

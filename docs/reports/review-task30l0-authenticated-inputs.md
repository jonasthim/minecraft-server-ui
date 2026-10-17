# Review: Task30L0 authenticated bootstrap inputs

## Summary

**PASS — no actionable source findings.** The change retains the exact metadata and signature bytes that are verified with the caller supplied public trust, writes them as private destination-owned files before archive acquisition, rechecks their identity and bytes before publishing the receipt, and leaves cleanup to the existing recorded-identity mechanism. The completion receipt remains the final output and its manifest digest is over the retained metadata bytes.

## Review evidence

- `release/bootstrap/prepare.go:156-168` clones the acquisition byte slices and authenticates them through `candidate.VerifyBootstrap` before any output is created. The public trust bytes are parsed from the caller-provided `TrustFile`; no trust key is copied into the prepared directory.
- `release/bootstrap/prepare.go:180-205,270-303` creates `bootstrap.json` and `bootstrap.sig` through the destination's exclusive `create` path, writes and syncs each file, and checks the ordinary-file binding and exact contents. Retention occurs before `BootstrapArchive` is called. The checks reject a replaced name or changed bytes.
- `release/bootstrap/prepare.go:241-267` checks the retained inputs again after extraction and before the receipt. The receipt is created exclusively, synced, closed, and is the last named output. `success` is set only after its final destination and cancellation checks.
- `release/bootstrap/files.go:188-234,248-268` confirms that ownership is recorded at exclusive file creation and failure cleanup removes only the recorded file identity. A substituted file is left in place and causes cleanup to report incomplete state.
- `release/bootstrap/prepare_test.go:366-431` verifies exact metadata/signature bytes including canonical trailing newlines, independently verifies them against caller-supplied trust, checks the receipt digest, output names, receipt bytes, and Unix private modes.
- `release/bootstrap/prepare_test.go:433-470,817-836` checks replacement preservation, removal of the other owned input, absence of a success receipt, and cancellation after confirming both authenticated files exist before archive acquisition.

## Verification

```text
go test ./release/bootstrap -count=1                                      PASS
go test -race ./release/bootstrap -count=1                                 PASS
go vet ./release/bootstrap                                                 PASS
GOOS=windows GOARCH=amd64 go test -c -o /tmp/minecraft-server-ui-bootstrap-review.test.exe ./release/bootstrap
                                                                            PASS (cross-compilation only)
git diff --check -- release/bootstrap/prepare.go release/bootstrap/prepare_test.go
                                                                            PASS
```

## Boundaries

## Integrator workflow composition

Root updated `.github/scripts/candidate-bootstrap-check.py` to require the exact
14/5 bundle members plus original `bootstrap.json`, `bootstrap.sig`, and final
`completion.json` (17/8 outputs). Both retained files must match the original
bytes and hashes; Linux metadata/receipt modes are0600 and output mode0700.
Existing complete-directory identity/hash preservation remains required.

Root ran the actual local Linux preparer with both producer format fixtures.
Preparation, development refusal, all member checks, and existing-output refusal
passed. Four independent controls remove metadata, alter metadata whitespace,
alter signature whitespace, and broaden the signature mode: all fail at
`extracted_members` without a retention-success claim. The existing member
corruption and same-size preparer corruption controls also fail as required.
No installed payload was executed. These are local composition fixtures, not
hosted or Windows evidence.

## Remaining boundaries

Windows cross-compilation does not exercise the protected child DACL or native file operations. Native Windows ACL execution and the candidate workflow using the root-owned updated checker remain required. This review covers the Task30L0 prerequisite only; it does not qualify the later privileged installer, installer transactions, or overall Task30 completion.

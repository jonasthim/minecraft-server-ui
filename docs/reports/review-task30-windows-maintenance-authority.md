# Independent review: Windows maintenance authority tokens

## Verdict

No source blocker found in the reviewed authority patch. Both call sites use the real current process primary token for user identity, elevation and group membership. The administrator membership check now duplicates that same token as an impersonation token with `TOKEN_QUERY` access at `SecurityIdentification` level before calling `Token.IsMember`.

## Findings

- `OpenProcessToken(CurrentProcess(), TOKEN_QUERY|TOKEN_DUPLICATE, ...)` captures the process token explicitly. `GetTokenUser` is read from this handle; the LocalSystem SID bypass remains first, and non-System callers must pass both `IsElevated()` and enabled Administrators membership.
- `duplicateMaintenanceMembershipToken` requests only `TOKEN_QUERY` on the duplicate and uses `SecurityIdentification` with `TokenImpersonation`. The original token and duplicate are each closed with a defer across success and every early return.
- The package-local native tests open the actual current process primary token, demonstrate that `IsMember` on that primary token fails, duplicate it with the production helper, compare the duplicate's user SID with the process SID, and verify an enabled group on the duplicate. The managerstate and selfupdate tests are independent and exercise both helpers.
- This matches the Windows API contract: `CheckTokenMembership` requires a non-null token handle with `TOKEN_QUERY` access that refers to an impersonation token; x/sys `Token.IsMember` forwards the token handle to `CheckTokenMembership` directly. See [Microsoft CheckTokenMembership documentation](https://learn.microsoft.com/en-us/windows/win32/api/securitybaseapi/nf-securitybaseapi-checktokenmembership) and [DuplicateTokenEx documentation](https://learn.microsoft.com/en-us/windows/win32/api/securitybaseapi/nf-securitybaseapi-duplicatetokenex).

## Verification and limits

Passed locally:

```text
go test -count=1 ./internal/managerstate ./internal/selfupdate
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/managerstate-authority.test.exe ./internal/managerstate
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/selfupdate-authority.test.exe ./internal/selfupdate
GOOS=windows GOARCH=amd64 go vet ./internal/managerstate ./internal/selfupdate
git diff --check -- internal/managerstate/maintenance_windows.go internal/managerstate/maintenance_authority_windows_test.go internal/selfupdate/maintenance_receipt_windows.go internal/selfupdate/maintenance_receipt_authority_windows_test.go docs/reports/task30-windows-maintenance-authority.md
```

The Linux tests do not execute Windows token APIs. Cross-compilation and vet do not execute the new native tests; hosted Windows CI remains necessary to confirm native behavior. No administrator/service/account state was changed locally.

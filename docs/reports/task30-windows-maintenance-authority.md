# Windows maintenance authority token handling

## Change

Both `managerstate.authorizeMaintenance` and `selfupdate.windowsMaintenanceAuthority` now open a real handle to the current process token with `TOKEN_QUERY | TOKEN_DUPLICATE`. They retain the existing LocalSystem bypass, token elevation check, Administrators SID check, and fixed error messages. For the Administrators membership check, each function duplicates that captured primary token as a `TokenImpersonation` token at `SecurityIdentification` level with `TOKEN_QUERY`, then calls `IsMember` on the duplicate. The opened process token and duplicate are closed with defers on all return paths.

The change addresses the `CheckTokenMembership` contract: a non-null token handle passed to it must refer to an impersonation token. `x/sys/windows.Token.IsMember` passes its token handle directly. Microsoft documents the requirement and notes that only a null handle triggers implicit duplication of the current token: [CheckTokenMembership](https://learn.microsoft.com/en-us/windows/win32/api/securitybaseapi/nf-securitybaseapi-checktokenmembership).

## Verification

Each package has a Windows-only native fixture that opens the actual current process token, confirms `IsMember` rejects that primary token, duplicates it through the production helper, verifies the duplicate retains the same user SID, and confirms an enabled group membership succeeds on the duplicate. The tests do not require administrator membership or change accounts, services, or privileges.

`GOOS=windows GOARCH=amd64` package compilation and Linux package tests are the local checks. These do not execute the native fixtures; hosted Windows CI remains the native verification source.

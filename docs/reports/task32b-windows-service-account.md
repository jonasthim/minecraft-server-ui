# Task 32B: normalize SCM local service-account lookup

## Finding and change

Run [39043683105](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39043683105) recorded the Windows schema-probe failure at the configured account SID lookup with Win32 error 1332 (`ERROR_NONE_MAPPED`). The probe already checked the configured service SID against the SID loaded from the protected account receipt; a successful lookup followed by an SID mismatch is a separate failure path.

The SCM harness resolver explicitly handles a local account written as
`.\user` by qualifying it with the machine name. The original run did not
retain the raw `ServiceStartName`, so this supports a narrow candidate fix but
does not prove that exact string was the value in that run. The Go probe now
applies that conversion only to the exact `.\username` form, using
`windows.ComputerName()` instead of an environment variable. Empty/NUL names
and malformed local forms fail before lookup. Other account names go unchanged
through `windows.LookupSID`. The resolved SID must remain an exact canonical
match to the expected account SID; no name-only fallback was added.

Microsoft documents that `GetComputerNameW` retrieves the local computer's
NetBIOS name, and that `LookupAccountNameW` with a null system name begins
translation on the local system. The implementation uses x/sys wrappers for
those APIs. See [GetComputerNameW](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-getcomputernamew) and [LookupAccountNameW](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-lookupaccountnamew).

## Tests and limits

Injected-resolver tests cover local-name qualification, unchanged qualified
and unqualified names, malformed names refused before lookup, exact SID
mismatch rejection, and lookup-error identity. A native Windows test resolves
the current token's account through the dot-qualified form when that token is
a local machine account; it skips only when the current account belongs to a
different domain. It does not create an account or service.

The tests were cross-compiled and vetted but not executed on Windows in this
work session. The next Windows updater run must confirm the configured SID
lookup passes; this change does not qualify the overall updater transaction.

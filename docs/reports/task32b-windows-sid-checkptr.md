# Task 32b: Windows SID checkptr failure

## Change

`managerProcessQuerySID` now validates the exact textual shape
`S-1-5-21-A-B-C-RID` before converting it to a Windows SID. Each variable
component must be canonical unsigned decimal within `uint32`, and the RID must
be at least 1000. It then uses `windows.StringToSid`, `IsValid`, and `String` to
confirm the Windows SID and exact round trip. It no longer calls
`SubAuthorityCount` or `SubAuthority`.

The pinned `golang.org/x/sys v0.48.0` implementation explains the race failure:
`windows.StringToSid` calls `SID.Copy`, which returns a `*SID` backed by a Go
byte slice. `SID.SubAuthorityCount` calls the generated
`getSidSubAuthorityCount`; that wrapper converts the native interior pointer
returned by `GetSidSubAuthorityCount` and dereferences it. Go 1.27's race
checkptr rejects that pointer arithmetic for the Go-owned SID. The textual
checks enforce the same required account boundary without accessing that
interior pointer. The native SID conversion and round-trip checks remain in
place.

## Tests and evidence

Added Windows test cases for the RID lower boundary and maximum `uint32`
components, plus rejection cases for built-in/virtual/privileged SIDs, short
forms, leading zeroes, signs, empty components, wrong case, overflow, and
whitespace.

- `GOOS=windows GOARCH=amd64 go test -c -o /tmp/task32b-selfupdate.test.exe ./internal/selfupdate` — passed.
- `GOOS=windows GOARCH=amd64 go vet ./internal/selfupdate` — passed.
- `git diff --check -- internal/selfupdate/native_windows.go internal/selfupdate/native_process_query_windows_test.go` — passed.
- The supplied hosted Windows log `/home/thim/.cache/dilla-tmp/ci-39041677923-windows.log` confirms the original checkptr stack at `SID.SubAuthorityCount` in `managerProcessQuerySID`.

## Boundary

Windows tests were cross-compiled but could not be executed on this Linux host.
The required hosted Windows race run is still needed to verify the original
failure is gone and the test cases execute under race/checkptr. This change does
not alter process ACL construction, grant rights, or DACL acceptance rules.

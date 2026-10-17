# Independent review: Task30L4A Windows ACL handling

## Finding

`platformManagerSID` treats every writable allow ACE for an `S-1-5-21-...` SID as evidence of the manager account, without checking the ACE flags. In particular, it accepts an `INHERIT_ONLY_ACE`. Windows defines that flag to mean the ACE does not control access to the object carrying the DACL; it only applies after inheritance. [Microsoft ACE_HEADER documentation](https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-ace_header)

This can select a SID which has no effective write access to the manager data directory. If it is the only qualifying domain SID, snapshot directories will then grant that unrelated SID full access and omit the actual manager SID. If another effective writable domain SID is present, the current code rejects the directory as having multiple writable account principals. Filter out inherit-only ACEs when discovering the effective writable account. Add a fixture with an inherit-only domain SID and a distinct effective manager SID, plus a case where the inherit-only SID is the only domain SID; neither should be selected as the effective manager.

## Review of parsing and exact DACL checks

- `privateAllowACE` checks the fixed SID header is present before reading its subauthority count, bounds the count to the Windows maximum of 15, checks the calculated SID extent against `AceSize`, and calls `SID.IsValid` only after those bounds. `GetAce` supplies the ACE pointer from the Windows-owned ACL. This ordering is appropriate for the flexible SID tail and avoids an unbounded read from the ACE.
- Native Windows CI run `39045481814` used the race-enabled CI test command and emitted no `checkptr` panic. Its managerstate package failed ordinary assertions instead. This is evidence against a checkptr failure in that run; cross-compilation alone does not qualify native behavior.
- `checkExactPrivateDACL` requires a protected, nondefaulted ACL with exactly three unique allow ACEs and exact SIDs, flags, and mask. It rejects inherit-only flags, deny ACEs, duplicates, and extra trustees. ACE order does not affect its result. The full mask is `0x001f01ff` and the creation descriptor uses matching `FA` rights.
- `maskGrantsWrite` recognizes content writes and security-control rights. It omits `FILE_DELETE_CHILD`, which allows deletion of entries within a directory. Consider including that directory right in the effective-write predicate if “writable account” is intended to include namespace modification.

## Native failure evidence

Run `39045481814` at `326876f`, job `117433228740`, failed `internal/managerstate` tests. The bounded log is `/home/thim/.cache/dilla-tmp/ci-39045481814-windows.log`. It contains `manager account identity could not be verified` in snapshot/verify tests and `directory DACL is not the exact private allowlist` in path-replacement/cleanup tests. The log records assertion errors, not a panic or checkptr diagnostic. The implementation report correctly says the log does not expose the serialized ACL that caused the original failure; it does not establish which descriptor detail caused these new failures either. The native directory fixture semantics still need to be corrected or directly inspected in a follow-up run.

## Verification

- `go test -race -count=1 ./internal/managerstate` passed on Linux.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/managerstate -o /tmp/managerstate-windows-review.test.exe` passed.
- No Windows source was changed as part of this review.

## Integrator follow-up

The implementation now ignores `INHERIT_ONLY_ACE` during manager SID discovery.
Native fixtures cover an effective manager alongside an inherited-only account
and reject an inherited-only account alone. Root also required
`FILE_DELETE_CHILD` in the write predicate because it permits directory entry
mutation; a direct predicate regression covers that right. Root reviewed both
corrections and reran managerstate/CLI race tests and vet successfully. These
close the source findings. Native Windows execution is still required to
establish whether the binary ACL interpretation resolves the original failure.

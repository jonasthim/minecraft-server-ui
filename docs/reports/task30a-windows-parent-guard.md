# Task30A native Windows parent guard test correction

Status: test correction submitted for independent review and a new native CI
run. This packet changes only `release/bootstrap/prepare_test.go` and this
report. The accepted acquisition implementation and its retained handles are
unchanged. No staging, commit, push, service, account, firewall, game, or local
native lifecycle operation was performed.

## Observed failure and cause

Native Windows job 117411673842 in [CI 39038021975](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39038021975)
at candidate `c41b2d6` failed only the bootstrap parent-substitution subtest.
The retained log is
`/home/thim/.cache/dilla-tmp/ci-39038021975-windows.log`, lines 4417–4428.
`prepare_test.go:558` attempted to rename `parent` to `parent-moved` from the
archive-acquisition callback while preparation retained the output directory and
each ancestor. Windows returned `Access is denied.` The test treated that denied
operation as a failure before testing the resulting safe state. The destination
replacement test and the other bootstrap subtests passed in that native run.

The test assumed the parent could be renamed while descendant handles were
retained. Windows imposes additional restrictions on renaming directory trees
containing open handles. This is documented in Microsoft's
[FILE_RENAME_INFORMATION rules](https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/ntifs/ns-ntifs-_file_rename_information).
Go's Windows `Root.OpenRoot` retains a directory handle and performs operations
relative to it; the reviewed local Go 1.27 source is
`/home/thim/.local/go/src/os/root_windows.go` and
`src/internal/syscall/windows/at_windows.go`. The corresponding official
[Go Root documentation](https://go.dev/src/os/root.go) explains how retained roots
bind operations to the opened directory. The native log establishes the actual
denial; the corrected test additionally requires the exact same rename to work
after preparation releases its handles, distinguishing that result from an
unrelated persistent ACL or privilege failure.

## Corrected gate

The original `parent substitution detected with retained cleanup` case still
attempts a real parent rename. A second case, `parent rename cancellation with
retained cleanup`, cancels immediately after that attempt. Neither skips.

If rename succeeds, on either OS, the test substitutes a distinct parent and
requires preparation to fail. It verifies cleanup through the original retained
tree, preserves the replacement's sentinel, and preserves an existing caller
file in the moved original parent. Linux continues to execute this substitution
path.

Only on native Windows, a permission-denied rename may take the guarded outcome.
Every other rename error remains fatal. This outcome must satisfy all of these
assertions:

- `parent-moved` was never created. The parent, output directory and existing
  caller file retain their original identities immediately after denial.
- With no cancellation, preparation succeeds through the unchanged retained tree;
  the output identity remains unchanged, the completion receipt matches validated
  metadata, every extracted member hash matches, and the private snapshot is gone.
- With cancellation, preparation fails and removes its entire output directory.
  Only the unchanged caller file remains under the parent.
- In both cases, the caller file retains its original identity and bytes. After
  `Prepare` returns, the same parent rename must succeed, preserve parent identity,
  and be reversible. A leaked handle or persistent permission failure fails the
  test.

These assertions exercise both useful completion after a denied attack and
failure cleanup after cancellation. No root is closed early and no share mode,
identity check, DACL, signature check, or product path rule is weakened.

## Local evidence and remaining native gate

The original native failure provides the red evidence: the old test unconditionally
fails on the observed Windows permission denial. The revised real-substitution
cases pass on Linux. Validation commands:

```sh
go test -count=1 -run '^TestOutputNoClobberAndPathBinding$' -v ./release/bootstrap
go test -race -count=1 ./release/bootstrap
go vet ./release/bootstrap
GOOS=windows GOARCH=amd64 go test -exec /bin/true ./release/bootstrap
git diff --check -- release/bootstrap/prepare_test.go docs/reports/task30a-windows-parent-guard.md
```

The focused Linux cases, package race suite, vet, diff check and Windows
cross-compilation pass. `/bin/true` deliberately avoids executing the Windows
test binary. The revised native Windows denial, completion, cancellation and
post-release rename assertions remain pending a new hosted run at the exact
reviewed correction. The prior failing native job does not qualify this revised
test, and compilation does not substitute for that run.

# Bootstrap retained-handle fixtures on Windows

Native run [39044310390](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39044310390), Windows job `117430492914`, checked commit `299943a3359973438578dceb733b9e523ba5967d`. The retained log at `/home/thim/.cache/dilla-tmp/candidate-39044310390-windows.log` showed three fixture expectation failures:

- Two close checks observed `GetFileType ... The handle is invalid` after cleanup. The handles were closed; the Windows error was not wrapped as `os.ErrClosed`.
- The destination replacement fixture's `os.Rename(output, moved)` returned `Access is denied` while retained file handles were open.

The affected code is test-only. Production creation pins and cleanup code remain unchanged.

`assertFileClosed` now checks both that `File.Fd()` is invalid and that `File.Stat()` fails after cleanup. This asserts closure across Unix and Windows without requiring one OS-specific error wrapper. The destination substitution test now accepts only a Windows permission denial, verifies the original directory identity remains at the original path and that the moved path is absent, then requires the same rename to succeed after `Prepare` returns and releases its handles. On platforms where the substitution succeeds, the original replacement-preservation assertions remain unchanged. No test is skipped and no production cleanup guard is relaxed.

The independent pin replacement fixture remains responsible for proving that a distinct replacement file is preserved when the operating system permits path replacement while the pin is open. The Windows directory fixture instead verifies that the held handles prevent substitution and are fully released after preparation.

## Verification

`go test -race ./release/bootstrap` passed on Linux. `GOOS=windows GOARCH=amd64 go test -c -o /home/thim/.cache/dilla-tmp/bootstrap-windows-test.exe ./release/bootstrap` compiled the Windows test binary, and `git diff --check` passed for the owned files. This does not claim a native Windows rerun; the run above is the failing baseline.

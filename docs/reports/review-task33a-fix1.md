# Task33A fix round 1 — independent scoped re-review

Reviewed fix: `00c83ca8bd862b54b5f5a67b26461363722c1018..1aad4cd22045bcad84e0c0fea341c32f999f9c1d`, using `.superpowers/sdd/2026-10-17-valheim-parity-completion/review-task33a-fix1.diff`. The working copies of the reviewed harness/client files match `1aad4cd`; later unrelated integration changes and the local preview were outside scope.

**SPEC: PASS — QUALITY: APPROVE, within this fix scope.**

**Addressed: 2 Important findings. Open: 0. New findings introduced by the fix: 0.** Original severities are retained. This closes G1/R1 and G2/R2 from `docs/reports/review-task33a.md` for source review; it does not close Task33 or qualify a host/game tuple.

## G1/R1 — Important: addressed

`deploy/qualification/linux-game.sh:18`, `:56` and `:65` now include the exact shipping installer scratch path `/usr/local/bin/minecraft-ui.new` in pre-ownership refusal, owned cleanup and final absence verification. The refusal remains before `owned=1` and before installation. Both ordinary existing files and dangling symlinks are rejected through the existing `-e`/`-L` checks. Cleanup removes only the fixed path after ownership is claimed; the final check makes a surviving regular file or symlink fail cleanup.

Reviewer ran the three new `LinuxGamePaths` tests against the actual static guard, cleanup operands and final absence fragment, with all operands mapped beneath `TemporaryDirectory`. Existing regular-file bytes and dangling symlinks survive refusal, owned partial output is removed, and a leftover changes the final status to failure. No installer, service, account or firewall command was executed.

To verify the regressions can fail, the reviewer reran those same tests with `game.HERE` pointing to a temporary copy of the original `00c83ca` Linux source. Result: **4 expected assertion failures across 3 tests**: both preexisting fixtures were accepted, owned partial output survived, and the final absence check was missing. The corrected source passes all three tests.

## G2/R2 — Important: addressed

`deploy/qualification/game_api.py:367–419` replaces the CIM datetime conversion with a native process-handle snapshot. The access mask `0x00101000` matches the shipping launcher's query-limited-information plus synchronize rights. PID binding, creation time and executable image use that one handle; liveness is checked before and after the queries, and `finally` closes the handle. `GetProcessTimes` returns the original FILETIME words. Formatting each word as unsigned eight-digit hexadecimal matches `internal/launcher/identity_windows.go:57` without rounding or tolerance. Python also validates the returned PID, exact image file binding and sixteen-digit timestamp before the existing exact creation-identity suffix comparison.

Reviewer reran the deterministic abstraction regression with FILETIME `134100000000000007`: the exact native value is accepted and its microsecond-truncated value is rejected as `native_start_mismatch`. Wrong PID, wrong image and unsuccessful native query are also rejected. These are mocked subprocess-result checks, not native Windows evidence.

The reviewer independently compiled the **actual C# text extracted from `WINDOWS_SNAPSHOT_SCRIPT`** with .NET SDK `10.0.112`'s Roslyn compiler and the installed `Microsoft.NETCore.App.Ref/10.0.12/ref/net10.0` reference assemblies: **exit 0, no diagnostics**. Command shape: `dotnet /usr/share/dotnet/sdk/10.0.112/Roslyn/bincore/csc.dll -nologo -noconfig -nostdlib+ -target:library -out:<temporary>/Snapshot.dll -reference:<each installed reference assembly> <temporary>/Snapshot.cs`. All outputs were temporary and removed. An initial SDK-project build with package sources explicitly cleared failed offline resolution of `Microsoft.AspNetCore.App.Ref (= 10.0.12)`; direct compilation avoided that local reference-pack limitation and performed no restore or network access. Neither compilation executes Windows APIs or parses the surrounding PowerShell.

## Other reviewer verification

- `python3 -B -m unittest discover -s deploy/qualification -p game_api_test.py -v`: **13 passed**.
- `npm test --prefix deploy/qualification/game-client`: **6 passed**, without a server connection.
- Python AST parsing of both changed Python files: **passed**.
- `bash -n deploy/qualification/linux-game.sh`: **passed**.
- `node --check` on `client.cjs` and `client.test.cjs`: **passed**.
- `git diff --check 00c83ca 1aad4cd`: **passed**.
- The added client `.gitignore` contains only `node_modules/`; no dependency/version change is introduced by this fix.

## Remaining native acceptance scope

`pwsh` is unavailable locally. Hosted PowerShell parsing/Add-Type execution, actual Windows handle access/serialization and exact creation matching against the installed native launcher remain required. Actual Ubuntu/Windows installation, native cleanup, real Vanilla 1.21.9 login/play/chunks, online backup, explicit restore and reconnect evidence still require an exact-source GitHub Actions run and integrator review. Broader versions/loaders, OS families, authenticated graphical clients and stable release support remain unqualified.

This review performed no hosted dispatch, services, accounts, firewall changes, local native game execution, game connection or user-preview action. Only this review report was written; implementation files were not edited, staged or committed. Source review permits the integrator's next hosted acceptance step; this report itself does not authorize or perform dispatch.

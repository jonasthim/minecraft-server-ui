# Task33A diagnostic/preparation packet — independent review

Reviewed exact source: `f6aef8308a22f343e4f1333bad5b54bf24332661..cb65058089da9260a1e472554bdaa3fae9c624a9`. Reviewer confirmed that `.superpowers/sdd/2026-10-17-valheim-parity-completion/review-task33a-diagnostics.diff` equals that Git diff byte for byte.

**SPEC: PASS — QUALITY: APPROVE for this diagnostic/preparation scope. New findings: 0. Open source-review findings: 0.** The previously closed Important G1/R1 and G2/R2 findings remain closed; their regression checks still pass. This packet does not qualify gameplay or close Task33A/Task33.

## Reviewed behavior

- **Failed-job diagnostics:** one authenticated request reads `/api/v1/jobs/{validated-id}/logs?after=0&limit=500`, matching the shipping logs route and maximum page size. The existing HTTP helper bounds response bytes and time. Only exact authored-message matches produce fixed enum strings; arbitrary text, near matches, exception details, paths and URLs are not retained. Codes are deduplicated and sorted. Retrieval and malformed/oversized-page failures produce fixed diagnostic codes while the original terminal job state and phase remain failures. Shipping lifecycle error redaction and product code are unchanged.
- **Fixed permission metadata:** `permission_snapshot` uses `lstat` on twelve hardcoded paths and emits only existence/type, numeric owner/group, mode, device and inode, plus a fixed read-failure flag. It reads no file contents or symlink targets. Four fixed calls bracket preparation and cleanup. The private history is included in the existing allowlisted `result.json`; the workflow artifact file allowlist is unchanged.
- **Linux prefix preparation:** the three guard/preparation/restoration functions match the already reviewed updater harness behavior exactly after excluding comments. Each fixed ancestor must be a root-owned nonsymlink directory, and ancestors other than the explicitly borrowed `/usr/local/bin` must already lack group/other write bits. Preparation records device/inode/UID/GID and exact mode, marks restoration necessary before attempting `chmod`, clears only `0022`, and verifies the resulting mode and binding. The installed supervisor's normal guards remain active.
- **Linux failure cleanup:** the EXIT trap is installed before prefix preparation. Cleanup attempts restoration even when installation ownership was never claimed or an earlier operation failed. Restoration refuses a symlink or changed directory binding, verifies the exact original mode/binding afterward, and makes cleanup fail on refusal or restoration failure. Existing owned-installation cleanup and `.new` refusal/removal/final-absence checks remain present. The diagnostic changes do not turn a failed operation into success.
- **Windows diagnostics:** Node and npm have separate fixed stages. The new artifact fields contain only `parameter_binding_validation`/`unclassified` and an integer source line bounded to `0..10000`; exception messages, invocation text and native command output are not added. Existing `finally` cleanup remains in place. This is diagnostic instrumentation, without a speculative correction to the previously unknown throwing statement.
- **Evidence wording:** the appended worker report preserves both jobs in run `39035607511` as failures before game qualification. It explicitly leaves the Linux installation cause and Windows throwing statement unknown. Prefix permissions are described as a possible precondition, without claiming that the prior artifact proved that cause. This re-review read the report; it did not independently download the hosted artifacts.

## Reviewer verification

- `python3 -B -m unittest discover -s deploy/qualification -p game_api_test.py -v`: **17 passed**. This includes exact/hostile/near-match log classification, unavailable and oversized diagnostic pages, metadata-only output, actual remapped shell fragments, and the earlier scratch-path and FILETIME regressions.
- Additional isolated checks extracted the actual preparation/restoration functions and replaced only `/usr/local/bin` with a temporary fixture path. Injected exit status `19` after preparation restored exact modes **04777, 06777 and 07777**, preserving the failure status. These complement the suite's **0777, 02777, 01777 and 0755** cases. A replacement ordinary directory, as well as the suite's replacement symlink, was refused without changing its mode.
- Independently compared all three prefix functions with `deploy/qualification/linux-updater.sh`: **identical behavior**.
- Python AST parsing of both changed Python files: **passed**.
- `bash -n deploy/qualification/linux-game.sh`: **passed**.
- `git diff --check f6aef83 cb65058`: **passed**.

All executed filesystem and shell mutation checks used temporary fixture paths. No privileged host prefix, service, account, firewall, game or local preview was touched. Only this review report was written; implementation files were not edited, staged or committed.

## Remaining hosted evidence

`pwsh` remains unavailable locally. Actual PowerShell parsing, exception classification and source-line reporting require the next hosted attempt. Native prefix metadata must demonstrate preparation and exact restoration on Ubuntu; installation and all subsequent real gameplay/backup/restore assertions must still pass on the exact reviewed source. A diagnostic run can fail again without establishing either earlier cause.

Source is approved for an integrator-owned hosted diagnostic rerun. This reviewer performed no dispatch. Full version/loader/host qualification and stable release claims remain open.

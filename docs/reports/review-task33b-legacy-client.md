# Review — Task33B legacy client qualification

## Finding

**R1 — Runtime evidence accepts an ID whose archive checksum differs from the verified manifest checksum.**

At `deploy/qualification/game_api.py:791-798`, `runtime_evidence` checks the
selected Temurin major, OS-shaped runtime ID, vendor, provider, release URL,
and internal manifest checksum fields. It does not require the 64-hex suffix
of `identifier` to equal `manifest["verified_sha256"]`. The package's runtime
ID contract constructs that suffix from the publisher archive checksum in
`internal/runtime/runtime.go:276`; manifest loading independently requires the
ID to equal the ID formed from `VerifiedSHA256` at `internal/runtime/runtime.go:524`.

The new 1.12.2 test fixture demonstrates the mismatch: its runtime ID ends in
`a` repeated 64 times while `verified_sha256` and the artifact checksum are
`b` repeated 64 times, yet `runtime_evidence` accepts it at
`deploy/qualification/game_api_test.py:68-80`. This allows a qualification
summary to claim publisher-verified runtime provenance without binding the
runtime ID to that verified archive. Compare the ID suffix against the
verified checksum and make the fixture's positive case consistent; retain a
negative case for the mismatch.

## Remediation submitted

`runtime_evidence` now requires the exact 64-hex suffix in the selected runtime
ID to equal the validated `verified_sha256`; the artifact checksum is already
required to equal that same digest. The positive Temurin 8 fixture now binds all
three values to the same digest. A new negative case changes the manifest and
artifact digest together while leaving the ID unchanged and requires the fixed
`managed_runtime_provenance` failure.

Targeted check: `python3 -m unittest
game_api_test.Helpers.test_runtime_evidence_binds_selected_temurin_major_and_official_source`
— pass after the regression failed before the production correction.

## Review coverage

The exact 1.12.2/Temurin 8 and 1.21.9/Temurin 21 tuples are frozen and passed
through the wrapper, API harness, client CLI, preview, backup checks, and
version-specific reports. The legacy client explicitly reports the absence of
`player_loaded`, chunk-batch, and `tick_end` support, omits the batch count,
uses the legacy look packet layout, and rejects unexpected batch events. The
modern path still requires chunk-batch evidence. I found no other concrete
false-evidence or unsafe-input issue in the reviewed legacy changes.

The existing PLAY/chunk/keep-alive/teleport/yaw, online backup and save-on,
safety restore, reconnect, manager restart, and deliberate-stop assertions
remain in the shared test flow. The wrappers preserve hosted runner and
installation guards, fixed loopback targeting, firewall restrictions, and
owned cleanup. The Node package manifest and lockfile dependency versions are
unchanged. Root-owned bounded diagnostic-enum changes were excluded from this
review.

## Checks run

- `npm test --prefix deploy/qualification/game-client` — 7 passed.
- `MINECRAFT_UI_TEST_PWSH=/home/thim/.cache/dilla-tmp/task33a-powershell-7.6.7/runtime/pwsh python3 -m unittest game_api_test.py` — 32 passed.
- `bash -n deploy/qualification/linux-game.sh` — passed.
- `python3 -m py_compile deploy/qualification/game_api.py deploy/qualification/game_api_test.py` — passed.

These are local source and fixture checks. They do not qualify a native legacy
game server or either hosted OS tuple.

## Verdict

**Approve.** The runtime ID suffix now matches the validated archive digest,
and the targeted test proves a matching fixture passes while a mismatched
digest is rejected. Focused re-review passed. Native legacy game qualification
remains pending hosted runs.

## Follow-up review — legacy setup and cleanup regressions

The failed hosted Linux legacy attempt recorded `api_http_status` at
`instance_create`; the same cleanup path then failed to read `$3` under
`set -u`. The current correction gives the typed draft a valid desired
`simulation_distance` of 2 for both tuples, while the product's versioned
`Properties` projection omits that key before Minecraft 1.18. The qualification
tuple continues to report the capability as unsupported for 1.12.2. This
preserves typed configuration validation without claiming the legacy server
supports the property.

The wrapper now copies the already validated version argument into
`minecraft_version` before defining `cleanup`, and reports that captured value
instead of reading the function's positional parameters. The new shell fixture
executes cleanup without function arguments for both exact versions and both
zero and failure exit statuses; it checks the retained report version/status,
restored report permissions, and removal of the owned scratch directory.

Independent verification on the current diff:

- `MINECRAFT_UI_TEST_PWSH=/home/thim/.cache/dilla-tmp/task33a-powershell-7.6.7/runtime/pwsh python3 -m unittest game_api_test.py` — 34 tests passed.
- `bash -n deploy/qualification/linux-game.sh` and Python byte-compilation — passed.
- `npm test --prefix deploy/qualification/game-client` — 7 tests passed.
- Existing `internal/instance/versioned_test.go` verifies that the generated
  legacy properties omit `simulation-distance`.

**Follow-up verdict: Approve.** These corrections address the observed setup
and cleanup failures without changing the approved 1.12.2/Temurin 8 and
1.21.9/Temurin 21 tuples or weakening the qualification guards. Actual hosted
legacy gameplay qualification remains pending.

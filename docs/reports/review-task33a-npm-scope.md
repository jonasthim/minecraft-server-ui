# Task33A Windows npm scope and bounded HTTP diagnostics — integrator review

Specification PASS; quality APPROVE; zero findings. Reviewed the four-file
working diff against8160174: windows-game.ps1, game_api.py, game_api_test.py and
task33a.md. Console/jobs admission is a separate packet.

The Windows change resolves caller-relative directory and private log paths
before npm.ps1 reparses arguments. It preserves exact ci/ignore-scripts flags,
installed dependency pins, exit checks, existing cleanup and hosted guards.
The new HTTP diagnostic full-matches only known method/route shapes and emits
fixed enums; unknown identifiers/paths/payloads/error bodies remain omitted.
No retry, timeout or backup safety rule changes.

Independent verification on2026-10-17:

- All21 game_api_test.py tests pass (0.520s), with the previously verified official
  portable PowerShell7.6.7 explicitly selected from disposable development cache.
- An in-memory pathlib read overlay supplies only the original HEAD Windows
  dependency block to the actual regression. It fails in Invoke-Expression with
  empty Path; the corrected real block passes. No product source is modified by
  this negative control. A skipped PowerShell test is not counted as evidence.
- The fixed route classifier and adversarial unknown path/payload regression pass.
- Source report accurately separates third-run Linux readiness/real PLAY and
  reconnect from the failed online-backup admission; it does not identify a
  specific background observer as the retained holder.

This is source/language-fixture evidence. Windows native script execution,
Linux backup admission correction and complete real backup/restore/restart
qualification remain pending. No local service/account/firewall/game or user
preview was changed. Source is accepted for an exact-source hosted rerun once
the observation-admission packet is independently reviewed.

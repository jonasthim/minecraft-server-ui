# Task32B runuser diagnostics — integrator review

Specification PASS; quality APPROVE; zero findings. Reviewed the eight-file
packet againstbcf0da7: manager probe/diagnostic test, Linux native adapter/schema
test, Linux qualification script, journal scrubber/test, worker report.
The worker reached its quota after implementation and focused race evidence;
the integrator independently completed verification before source acceptance.

The child marker is emitted only after exact command/argument selection of
update-schema. Typed stdout and returned errors remain unchanged. The adapter
retains the existing8KiB stderr bound, relays at most one entry plus one failure,
and reports the original command failure/invalid JSON. Entry is never success.
Wrapper and PAM classification use exact finite source-authored strings; only
fixed enums enter artifacts, with unknown/extended/localized text discarded.
The fixed package-version query is read-only. No unit, PAM, account, runuser
command, activation, authorization, retry or timeout guard changes.

Independent verification:

- Focused Linux race tests pass: selfupdate4.130s, manager5.188s.
- Five real scrubber subprocess tests pass0.079s; Bash syntax and diff check pass.
- Linux vet and Windows vet pass for affected commands/selfupdate; Windows
  selfupdate/qualifier test binaries compile. Compilation is not native execution.
- A temporary overlay replacing only classifyRunuserFailure with the original
  no-classification behavior fails the real exact-message regression.
- A second overlay removing only the new update-schema entry emission fails
  the command-specific entry regression. No working source was changed.

Actual39038823522 still establishes only schema_probe/command:exit:1 onUbuntu24.
This accepted diagnostic packet must run on the exact hosted candidate to select
a cause. No local service/account/firewall/game or preview changes occurred.

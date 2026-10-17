# Task33A stopping-process and Windows firewall diagnostics review

Integrator specification/quality review after fix round1: PASS, zero remaining
findings. This accepts diagnostic source only, not either failed hosted case.

Root found four omissions in the first packet: absent booleans became false,
zero exit timestamps appeared present, watchdog categories were absent, and a
file-validation exception could replace the primary failed job. The correction
requires typed identities/booleans, distinguishes absent/zero/malformed exit
records, reports only exact authored watchdog categories, and makes diagnostics
best effort while preserving the failed lifecycle job. File reads now cap actual
reads at32769bytes and check ordinary-file identity before/after reading.

Root compared the mappings with lifecycle safeFailure and launcher Exit, process
identity, stop-intent and finalizeExit source. Fixed identity/stop/exit paths are
the only new reads. Raw paths, tokens, exception messages, watchdog text and
configuration never enter artifacts. Unknown text is unclassified. Diagnostics
do not change the outcome of acceptance, native stop, restore or firewall rules.

Windows CimException collection accepts only three explicitly named numeric
properties, bounds their values, rejects strings/booleans/overflow, and is invoked
only after the existing runtime type-name check. It retains no Message or raw
CIM error data. Root reviewed the unchanged firewall expression separately.

Root reran all27 game harness tests using the independently verified portable
PowerShell7.6.7 executable. All passed, including the actual numeric collection
function, its negative type/bound cases, missing/invalid launcher data,
zero-timestamp handling, exact watchdog classification and preservation of the
primary job failure. Worker Python compilation, PowerShell parse and diff check
also passed. Hosted Windows/Linux runtime results remain required; no local
service, account, firewall, game or preview was operated.

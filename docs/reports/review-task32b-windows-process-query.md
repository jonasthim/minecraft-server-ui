# Task32B process query — integrator review

Specification PASS for source and qualification composition; quality APPROVE;
zero source findings established. Reviewed the worker's native Windows process
security code/tests, qualifier platform files and root command/harness wiring.
The worker's quota interruption is recorded; root independently finished the
source review and available verification before this checkpoint.

Production authority comes only from the protected account receipt and own
process handle. The strict merge adds exactly query-limited0x1000, preserves
trusted permissions/denies and verifies readback. Invalid state or grant failure
prevents Running. No client can choose a PID, SID, process path or permission
mask. No identity verification/activation authorization was removed. The new
qualification command only opens handles, never exercises mutation rights;
unexpected success or an error other than exact ACCESS_DENIED fails evidence.
Its public output is reconstructed from a bounded exact typed schema.

Windows selfupdate and qualifier test binaries compile; Windows vet passes for
selfupdate, qualifier and manager. Linux qualifier race passes1.031s; all12
existing updater API tests pass. Pure PowerShell parser valid/eight negative
controls and full-script parsing pass. Native unit tests and real service DACL,
manager-account limited-query/stronger-right denial, installed activation and
recovery remain required and are explicitly not claimed from compilation.

The original live failure was process_open/access denied. This source correction
is accepted for an exact-source hosted test, not as proof that all Windows
activation gates now work. If the real default descriptor fails the conservative
merge/readback policy, inspect that specific evidence before relaxing it.

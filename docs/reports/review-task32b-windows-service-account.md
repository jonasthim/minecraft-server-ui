# Integrator review: Windows service-account lookup

Source review passes for this narrow correction. The resolver transforms only
SCM's dot-qualified local account spelling, uses GetComputerNameW through
x/sys rather than environment input, and requires the resolved canonical SID
to equal the protected account receipt. Other names retain the existing Windows
lookup behavior. Malformed local input, missing lookup results and SID mismatch
fail; there is no name-only or alternate-principal fallback. Existing service
binding, expected unprivileged account, recovery-action and process checks remain.

The actual run39043683105 identifies LookupSID failure1332, and the harness
resolves the installed identities using local-name normalization. It does not
record the raw SCM name, so the attribution to dot-qualified spelling remains
an inference until the next native result.

Root independently passed Windows amd64 test-binary compilation and Windows
go vet. Injected Windows tests cover normalization, unchanged other forms,
invalid input, mismatch and failure propagation. The native token-account test
must fail unexpected API errors and skips only a nonlocal domain account; its
Windows execution remains pending. This does not qualify installed activation.

# Review: reopening authenticated installer inputs

## Finding and correction

The first implementation applied the one MiB auxiliary-asset limit to `selfupdate`, which is a native executable. This would reject real installer workers despite passing small fixtures. The correction uses the same first-two-members binary limit rule as the signed candidate layout and adds an authenticated updater larger than one MiB to the import, close, reopen and member-read regression.

## Accepted scope

`OpenRecorded` opens only the fixed transaction `inputs` directory. It validates the protected directory and file metadata, independently supplied trust, exact signed manifest digest/version/platform, completion receipt and member set; it rehashes the pinned members and rechecks identities. It performs no writes, cleanup, service operations or execution. Existing member access now also verifies the expected mode. Import behavior remains intact.

The worker may use this API before continuing a prepared apply. Explicit rollback does not depend on unexpired target metadata or on reopening target inputs.

## Evidence

- Integrator `go test -race ./internal/installinputs` and `go vet ./internal/installinputs`: passed after the size-limit correction.
- Reviewed fixtures cover the large executable, altered metadata/content/modes, unexpected files, pinned metadata replacement, transaction replacement and filesystem preservation on refusal.
- Windows compilation was reported by the implementer; this is not native Windows qualification.

**Approve for the scoped input-reopen API.** Native installer lifecycle acceptance remains separate.

# Task30 recorded installer input reopening

## API and behavior

Added `installinputs.OpenRecorded(ctx, transaction, RecordedOptions)`. It accepts an exact version, independently supplied trust, development policy, and expected manifest SHA256. The only source location is the fixed `transaction/inputs` entry; callers cannot select a path.

The reader validates the transaction and inputs directory bindings, exact private directory mode and owner, the bounded exact file set, single-link regular files, metadata file modes, and the platform member modes. It verifies the signed bootstrap with current validity, exact version, platform and manifest digest; decodes the strict completion receipt; and streams every member through its signed size and digest. It pins all opened files and the inputs root for the existing `OpenMember` API, rehashes the retained files during final identity validation, and rechecks the transaction/input directory bindings after I/O. It performs no creation, copy, deletion, or lock operation.

The Linux implementation enforces root ownership and Unix modes. The Windows build uses the existing protected DACL checks; this is compile evidence only, not native Windows qualification.

## Verification

- `go test ./internal/installinputs -run 'TestOpenRecorded' -count=1` — passed.
- `go test ./internal/installinputs -count=1` — passed.
- `go test -race ./internal/installinputs -count=1` — passed.
- `go vet ./internal/installinputs` — passed.
- `GOOS=windows GOARCH=amd64 go test -c -o /tmp/task30-installinputs.test.exe ./internal/installinputs` — passed as cross-compilation only.

Temporary fixtures verify Import → Close → OpenRecorded → OpenMember, tampered manifest and member refusal, foreign entry refusal, unsafe mode refusal, identical-byte pathname replacement after pinning, transaction-root replacement refusal, and unchanged filesystem trees on failed inspection. No native service, account, manager, or game state was touched.

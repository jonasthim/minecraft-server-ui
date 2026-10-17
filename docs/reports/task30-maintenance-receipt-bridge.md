# Task30L1B maintenance receipt bridge

## Scope and source findings

Implemented the frozen privileged receipt and lock prerequisite only. This
packet adds no CLI, web route, service operation, account operation, installer
or lifecycle orchestration. Root retains integration and staging.

The frozen brief names launch-permit.json; the shipped Linux updater actually
uses launch.json and launch.pending (internal/selfupdate/launch_linux.go,
readLaunchAt and writeLaunch). The conflict scan uses those actual names.
It also refuses activation.json, activation.pending, state request.json,
installed.pending, and installation-receipt.pending, plus
selfupdate/control/request.json when that canonical safe directory exists.
The scan fails closed on unsafe ancestry or other open errors and does not
delete or reinterpret existing state.

Production acquisition authenticates before filesystem access (Linux effective
UID 0; Windows LocalSystem or an elevated token that belongs to Administrators).
It opens the fixed protected state and binary roots, checks the canonical
manager executable, rechecks root identity, then takes the existing
launcher.AcquireManager(paths.State) lock. It retains the roots, lock, and
receipt/publication descriptors through Close. It does not create state/bin
directories or remove the lock file.

Receipt captures either absence or a validated installed.json snapshot.
Present snapshots retain the opened file handle and compare stable file
metadata, path identity, exact bytes, and digest. Parsing enforces the same
format, stable-version, schema, and lowercase-hash conditions as
readInstalled (internal/selfupdate/import.go). CAS requires a live snapshot
owned by the same session, rechecks conflicts and the canonical executable
hash, writes an exclusive bounded installation-receipt.pending, validates
the retained pending handle and path, rechecks the captured prior immediately
before rooted rename, and invokes the platform durability fence. A fence error
after rename reports publication uncertain; unknown pending replacements are
preserved.

The new implementation is limited to:

- internal/selfupdate/maintenance_receipt.go
- internal/selfupdate/maintenance_receipt_linux.go
- internal/selfupdate/maintenance_receipt_windows.go
- internal/selfupdate/maintenance_receipt_other.go
- internal/selfupdate/maintenance_receipt_test.go

## Verification

Linux package checks passed:

- go test ./internal/selfupdate
- go test -race ./internal/selfupdate
- go vet ./internal/selfupdate
- Windows compile: GOOS=windows GOARCH=amd64 go test -c -o /tmp/selfupdate-windows.test.exe ./internal/selfupdate
- git diff --check on the bridge files

Acceptance coverage includes missing-to-new and exact-prior-to-next CAS;
exclusive same-process and cross-process updater-lock acquisition; cancellation
cleanup; stale same-size receipt-byte changes; replaced receipt identity;
foreign and closed-session receipts; pending collision and unexpected pending
replacement preservation; unsafe receipt and executable; executable hash
mismatch; short-write cleanup; post-rename fence failure; and idempotent
Close with released lock. A table-driven matrix now exercises every adjudicated
conflict filename at acquisition and when introduced immediately before CAS,
asserting refusal and preservation of both the conflict bytes and installed
receipt. It includes the state and control request paths, actual Linux
launch.json/launch.pending names, an absent safe control directory, and
symlinked unsafe control ancestry both at acquisition and when introduced
before CAS.

The stale same-size-byte regression was also run with an isolated Go source
overlay that bypassed compareCurrentReceipt; the test failed with its
expected “same-size receipt mutation accepted” assertion, confirming that it
detects loss of the CAS comparison.

## Qualification boundary

Windows received a cross-compile only. Windows native ACL, retained-handle,
rename-over-existing, and durability behavior still require the native Windows
CI/runtime gate. Linux local temporary-file tests do not qualify the installed
root-owned layout. Production acquisition also validates the canonical
installed manager executable, so this capability does not cover a fresh
installation before that executable exists. Fresh install/reinstall authority
needs a separately reviewed capability. This packet does not complete
installer lifecycle acceptance or the project implementation ledger.

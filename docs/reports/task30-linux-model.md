# Task30 Linux intent and inventory model

## Scope delivered

Added Linux-only typed intent and inventory validation in `internal/install/model_linux.go`, with boundary tests in `internal/install/model_linux_test.go`. The functions are pure data operations: they do not inspect files, accounts, services, manager state, or host configuration.

Intent and inventory JSON is bounded, rejects invalid UTF-8, duplicate keys including escaped-equivalent names, unknown fields, missing required fields, null required values, and trailing data. Image and transaction bindings use the existing validators. Asset entries must follow the fixed fourteen-role order and match the fixed modes, hashes, sizes, and native identity rules.

The validator covers fresh and managed apply, retained-data reinstall, and uninstall. It binds the manager asset to the target/current receipt, binds the apply executor to the authenticated updater asset, parses bootstrap trust against the fixed repository, and requires bootstrap trust preservation. It permits a recorded missing deployment asset in apply intent for a driver-authorized repair and allows the inventory's manager image to lag a validated ordinary updater receipt.

Uninstall intents encode empty bootstrap trust as an empty base64 string. `marshalLinuxIntent` normalizes a nil slice on a shallow owned copy, so callers can construct a zero-value payload without mutation while the strict decoder continues to reject an explicit JSON `null`.

## Driver obligations

This packet supplies no native driver or lifecycle qualification. Before native effects, the driver must independently:

- Authenticate the imported manifest, bootstrap trust source, executor and their captured digests. Parsing public trust does not prove a release signature.
- Prove the fixed named account identity and make all decisions from native lookup results, never JSON usernames or paths.
- Validate that any ordinary updater receipt superseding inventory is authentic and terminal, then capture the actual manager image and identities.
- Require explicit repair authorization before acting on any deployment asset that is recorded as missing from the prior inventory.
- Establish that a fresh install has no ambiguous manager data, and enforce service, manager data-lock, updater-lock, game activity, and rollback preconditions.
- Treat `AllowDevelopment` as a trust-mode input only; it does not bypass any model or native validation.

## Evidence

The package test, race, and vet commands passed:

```text
/home/thim/.local/go/bin/go test ./internal/install -count=1
ok   github.com/jonasthim/minecraft-server-ui/internal/install
/home/thim/.local/go/bin/go test -race ./internal/install -count=1
ok   github.com/jonasthim/minecraft-server-ui/internal/install
/home/thim/.local/go/bin/go vet ./internal/install
```

Regressions cover fresh/managed/retained apply and uninstall, nil-trust uninstall marshal/decode round trip without input mutation, explicit null rejection, current-receipt supersession, malformed or oversized JSON, missing/null/duplicate/unknown fields, schema and transaction mismatches, ordered asset inventories, ownership identities and modes, manager digest binding, executor binding, bootstrap trust preservation, and target asset completeness.

These are deterministic package checks only. No native Ubuntu run, service/account operation, asset publication, or installer lifecycle acceptance is claimed. The integrator must review this packet and update the shared implementation ledger before marking it accepted.

# Review: Task30 Linux intent and inventory model

## Summary

The model follows the packet's pure-data boundary and the root clarifications for updater receipt supersession and repairable missing assets. One round-trip defect remains for uninstall intents whose empty bootstrap trust is represented by a nil slice.

## Round-one review

The fix normalizes nil uninstall `BootstrapTrust` on a local copy before marshaling. The regression covers marshal/decode round-trip and confirms the caller's intent remains unchanged. The decoder still rejects explicit JSON null through its required-field preflight; the existing malformed-input tests cover required null values, though they do not target `bootstrap_trust` by name.

No new product breakage found.

## Clarifications checked

- Uninstall manager `Before` is bound to the current `Prior` receipt digest while the other preimages match the inventory exactly.
- Apply accepts an absent captured preimage for a previously present nontrust asset, leaving repair authorization to the native driver. Manager and trust assets do not use this exception.
- The model parses supplied bootstrap trust against the fixed repository but makes no signature or native ownership claim.

## Evidence and verdict

The final package tests, race, and vet are reported as passing. I did not rerun them during this scoped review.

**Verdict: Approve.**

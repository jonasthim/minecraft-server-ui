# Task33A — official MSMP discovery parameter arrays

Status: scoped product compatibility correction and bounded harness diagnostics implemented; independent review and exact-source hosted gameplay acceptance remain pending. No game/native service was executed locally, and nothing was staged, committed or dispatched by this worker.

## Verified incompatibility

The shipping `rpcMethod.Params` decoder expected a JSON object pointer. Official Minecraft1.21.9 discovery encodes that field as a JSON **array**: zero descriptors for a parameterless method, one descriptor for a method with one parameter. As a result, the shipping decoder returns an unmarshal error for the real discovery shape and rejects it as unsupported before world-readiness verification. Existing synthetic readiness/player fixtures incorrectly supplied an object or omitted the field, so they concealed the incompatibility.

This is a deterministic protocol compatibility defect, consistent with run39036770835 failing at `verifying_game_readiness`. The prior artifact does not contain the original discovery response or underlying error, so static/probe evidence establishes the defect without claiming it was that run's only failure or that subsequent real-game paths now succeed.

The [official1.21.9 release notes](https://www.minecraft.net/en-us/article/minecraft-java-edition-1-21-9) document authenticated MSMP, discovery and the equivalent data-generator schema. To verify the exact wire shape without executing server code, the worker independently inspected the root-provided cached official artifacts:

- `sha1sum /home/thim/.cache/dilla-tmp/minecraft-official-1.21.9.jar` returned `11e54c2081420a4d49db3007e66c80a22579ff2a`, matching the approved official server tuple and its hosted lifecycle preview.
- Python ZIP inspection read only `META-INF/versions/1.21.9/server-1.21.9.jar`, then the relevant class bytes. They exactly matched the cached static-inspection classes. `ats.class` SHA256: `dfec0c0b4ed3148db2afc0d59ac9beb9160497af1efa000afa3ba7ff12626e30`; `ati$d.class` SHA256: `9dbb9bccd08303110c8da3da1d58a036c434b074c46a44849a187fac89eff872`.
- The supplied official mappings identify `ats` as `net.minecraft.server.jsonrpc.api.MethodInfo`, `att` as `ParamInfo`, and the parameter descriptor's name/schema/required fields. `javap -c -p .../ats.class` shows the parameter descriptor codec converted with `listOf()` and mapped to an optional descriptor, then bound through required `fieldOf("params")`. The optional encoder supplies an empty list or singleton list. This proves the discovery field's array representation.
- `javap -c -p '.../ati$d.class'` independently shows method requests accept a JSON array **or named object**, retrieving the declared name for the object case. Therefore the existing named request maps remain valid; changing outgoing requests was unnecessary and outside this correction.

Neither `javap` nor Python ZIP reading loads/executes Minecraft classes. The data generator, server main and game runtime were not run. No additional official jar or runtime was downloaded locally for this work.

## Product change and guard preservation

`internal/console/rpc.go` now decodes `params` into `[]rpcParam`, with typed name/schema/required descriptors. Discovery rejects a missing/null parameter list and invalid descriptor names. Capability matching requires exactly zero descriptors for a parameterless request or exactly one matching descriptor for a parameterized request. An additional or duplicate descriptor cannot be silently ignored, and the old incorrect object shape is rejected by typed JSON decoding.

Unrecognized extended methods may be discovered, but they cannot grant a used capability with an unexpected parameter count/name. Existing result-name matching remains required. The WebSocket authorization header, fixed loopback endpoint, protocol-version check, size/method bounds, duplicate-method check, random request IDs, notification handling, response-envelope validation, operation deadlines and before/after JVM-generation guards are preserved. No public API contract, CSRF/role rule, request map, dependency or authentication setting changed.

Readiness and player fixtures in `control_test.go` and `players_test.go` now use empty/singleton descriptor arrays. `service_test.go` adds an actual session-level1.21.9/protocol1.0.0 readiness/readback test using that corrected fixture. Existing generation-change and malformed-response tests continue to run against the corrected wire shape.

The new `TestMSMP1219OfficialDiscoveryShape` is an authenticated synthetic WebSocket probe, with provenance tied to the official jar above. It reproduces the observed **shape**; it is not a schema dump captured from a running server and is not real-game acceptance. Signature tests cover zero/one, wrong/missing/extra/duplicate descriptors, nulls, empty descriptors, old object form and scalar forms. Discovery admission tests reject malformed/missing/null lists and invalid descriptor names before operations can use them.

## Bounded readiness failure diagnostics

The owned API harness now makes a read-only `/console/status` request after a failed job and at a readiness timeout. It uses an independent **five-second** request timeout, preserving existing request defaults and the original failed job state/phase. Only literal booleans from six named fields, an allowlisted status and an **exact known-reason enum** are retained. Missing/malformed/unavailable status and unknown reason text remain explicit fixed codes; strings such as `"true"`, integers, arbitrary reasons, source paths, credentials and extra fields are not copied or coerced.

The classifier is diagnostic only and cannot convert an unsuccessful product operation into success. No raw logs, schema, HTTP bodies, native reason text, management secrets or arbitrary API fields enter artifacts. The previously reviewed Windows catch correction at9ee62da is preserved.

## Red/green verification

Before changing the product decoder, corrected official-shape fixtures and new focused tests were run:

```text
go test ./internal/console -run 'TestMSMP1219OfficialDiscoveryShape|TestMSMPDiscoveryArraySignatures' -count=1
FAIL: official discovery array shape rejected as unsupported;
      json: cannot unmarshal array into Go struct field rpcMethod.params.
      The old object-shaped fixture was incorrectly accepted.
```

After the decoder correction:

```text
go test ./internal/console -run 'TestMSMP1219OfficialDiscoveryShape|TestMSMPDiscoveryArraySignatures' -count=1
PASS (0.004s)
go test ./internal/console -count=1
PASS (0.480s)
go test -race ./internal/console -count=1
PASS
go vet ./internal/console
PASS (exit0)
python3 -B -m unittest discover -s deploy/qualification -p game_api_test.py -v
Before diagnostic implementation: Ran19; FAILED (failures=1, errors=1).
After diagnostic implementation: Ran19; OK (0.221s).
python3 -m py_compile deploy/qualification/game_api.py deploy/qualification/game_api_test.py
PASS (exit0)
git diff --check -- internal/console/rpc.go internal/console/control_test.go internal/console/players_test.go internal/console/service_test.go deploy/qualification/game_api.py deploy/qualification/game_api_test.py
PASS (exit0)
```

The Go tests use bounded in-process HTTP/WebSocket fixtures and no external network or game. The full console package run includes preserved fresh-request, malformed/oversized/timeout, player readback and generation-change checks. Python tests verify exact reason classification, unknown/malicious suffix rejection, strict boolean handling, five-second diagnostic timeout and unavailable/malformed status behavior. No local services, accounts, firewall changes or user-preview operations were performed.

## Remaining evidence

Independent source review, root-owned integration and another exact-source hosted run are required. Real Vanilla readiness, actual PLAY/chunks, online save lease, archive/restore markers, native restart and Windows dependency setup remain governed by the original Task33A assertions. This correction does not qualify other versions/loaders or close Task33/stable-release acceptance.

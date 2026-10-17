# Task33A MSMP discovery correction — independent scoped review

Reviewed exact source: `59dca2e15d253778c786fd5374e30a34fc069bd5..b0ede67cb12db03f8d47ec0a46bdf865b8ea6937`. The frozen `.superpowers/sdd/2026-10-17-valheim-parity-completion/task33a-msmp-review.diff` matches that Git diff byte for byte. The reviewed implementation/test files match the candidate; concurrent outer-package work is outside scope.

**SPEC: PASS — QUALITY: APPROVE. Findings: 0.** This approves the narrow discovery decoder, corrected fixtures and bounded harness diagnostics. Earlier Task33A findings remain closed. Actual hosted readiness/gameplay acceptance remains open.

## Independent official-contract evidence

Reviewer recomputed SHA1 of the cached official `minecraft-official-1.21.9.jar`: **`11e54c2081420a4d49db3007e66c80a22579ff2a`**. Python ZIP inspection read the nested `META-INF/versions/1.21.9/server-1.21.9.jar` and verified that both inspected classes exactly match its bytes:

- `ats.class`: SHA256 **`dfec0c0b4ed3148db2afc0d59ac9beb9160497af1efa000afa3ba7ff12626e30`**.
- `ati$d.class`: SHA256 **`9dbb9bccd08303110c8da3da1d58a036c434b074c46a44849a187fac89eff872`**.

The provided official mappings identify `ats` as `net.minecraft.server.jsonrpc.api.MethodInfo` and `att` as `ParamInfo`, with descriptor name/schema/required fields. Reviewer independently ran `javap -c -p` on the cached classes and `javap -v -p` on `ats.class`:

- `MethodInfo` derives its parameter codec through `ParamInfo.codec().listOf().xmap(...)`, then uses required `fieldOf("params")`. Its encoder maps a present descriptor through the singleton `List.of(Object)` bootstrap target and defaults an absent descriptor to `List.of()`. Discovery therefore contains an empty or singleton **array**, including the field for parameterless methods.
- `ati$d` accepts request parameters as either a JSON array or a JSON object. The object branch looks up the declared parameter name; the array branch requires exactly one element. Existing named request maps remain compatible and need no change.

This independently substantiates the decoder incompatibility. Reading ZIP entries and disassembling bytes does not execute Minecraft classes; no server, data generator or game was run.

## Product behavior and preserved guards

`internal/console/rpc.go` now uses `[]rpcParam`, matching the official discovery representation. Missing/null lists and invalid descriptor names are rejected at admission. Used capabilities require an exact zero-parameter or one-matching-parameter signature and the existing exact result name. Extra/duplicate parameters cannot grant a used capability. The old object-shaped discovery representation fails typed decoding.

The product diff changes descriptor representation/admission and capability matching only. It preserves the fixed loopback endpoint, private Bearer authentication, protocol-version comparison, response/method bounds, duplicate-method rejection, fresh random request IDs, notification/envelope checks, deadlines, and generation checks around requests/results. Readiness still requires the fresh server-status acknowledgement and exact required gamerule verification/readback; it is not inferred from discovery or heartbeat. No outgoing request map, dependency, role/CSRF boundary or authentication default changes.

The readiness/player fixtures now use empty/singleton arrays. The new session test exercises the real console session path with authenticated synthetic protocol `1.0.0` discovery and required PvP readback for Vanilla 1.21.9. These are source fixtures, not a captured live schema or real-server acceptance.

## Failure-diagnostic behavior

The harness requests only the existing authenticated GET `/console/status` endpoint after an unsuccessful job or readiness timeout. Reviewer inspected its shipping handler and `Service.Status`: this is read-only observation, without applying settings or issuing native mutations. The diagnostic request passes timeout `5`; ordinary requests retain default `30` and the existing response byte limit, fixed origin, CSRF/session handling, redirect refusal and disabled proxies.

Output contains an allowlisted status, exact known-reason code, availability flag and actual booleans from six fixed fields. Arbitrary reason suffixes, source fields, credentials and additional response fields are excluded; missing/unavailable data retains fixed diagnostic codes. The original unsuccessful job state/phase or timeout still causes failure. Diagnostics cannot qualify readiness or convert the original operation to success.

## Reviewer verification

- Used a temporary Go `-overlay` to replace only `internal/console/rpc.go` with its **59dca2e** version while retaining the candidate regressions. `TestMSMP1219OfficialDiscoveryShape` failed as unsupported; zero/one array signature cases failed JSON unmarshalling, null was incorrectly usable, and the old object shape was incorrectly accepted. The same focused command without the overlay **passed**. No implementation file was modified.
- `go test ./internal/console -run 'TestMSMP1219OfficialDiscoveryShape|TestMSMPDiscoveryArraySignatures' -count=1`: **passed**, 0.004s.
- `go test -race ./internal/console -count=1`: **passed**, 11.977s, including malformed discovery, fresh-response/readiness, exact rule/player readback and generation-change regressions.
- `go vet ./internal/console`: **passed**.
- `python3 -B -m unittest discover -s deploy/qualification -p game_api_test.py -v`: **19 passed**, including strict diagnostic output/timeout checks and previous scratch-path, prefix-mode and FILETIME regressions.
- Python AST parsing of both changed Python files and `git diff --check 59dca2e b0ede67`: **passed**.

## Remaining acceptance

The decoder defect is proven independently of the earlier hosted failure. The prior artifact did not retain the original discovery response/cause, so this review does not establish that it was the only cause of run `39036770835` failing or that its correction completes readiness. A new exact-source GitHub run must verify actual native readiness, client PLAY/chunks, online backup/save lease, explicit restore/reconnect and native restart on the required initial hosts. Other tuples and stable release support remain unqualified.

Only this report was written. No implementation edits, staging/commit, hosted dispatch, service/account/firewall changes, local native game execution, game connections or user-preview actions were performed.

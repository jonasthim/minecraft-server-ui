# Task 33a: hosted Windows firewall result

## Run and retained evidence

The isolated [Windows game firewall investigation run 39043681496](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39043681496), job `117428055072` (“Compare equivalent loopback exclusions”), completed successfully on commit `92d0acd2a332bc1ad57c3e01526364ac458a81de`. The run executed the firewall probe without starting a game. Its retained artifact, `windows-firewall-92d0acd2a332bc1ad57c3e01526364ac458a81de/firewall-summary.json`, was downloaded to `/home/thim/.cache/dilla-tmp/task33a-firewall-run39043681496-native/` and contains only the probe's fixed case labels, booleans, and numeric CIM codes. The artifact SHA-256 recorded by GitHub is `716592e289bbfc0f8ed060836e7967e9e5ca1ea08a727dd016871b1c9add663d`.

The `original` case and `cidr_equivalent` case both report:

- `created: false`
- `hresult: [-2146233088]`
- `native_error_code: [1]`
- `status_code: [1]`
- `exact_match_normalized_address_set: false`
- `cleanup_verified: true`

The original case uses the same two IPv4 ranges and `::/0` as the game harness. The CIDR case uses the eight IPv4 CIDRs that cover every IPv4 address outside `127.0.0.0/8`, plus the unchanged `::/0`. Since neither rule was created, neither case produced an address-filter readback. The successful cleanup checks confirm no probe rule remained.

This matches the earlier game acceptance failure in [run 39042942229](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39042942229), job `117425924394`, which recorded the same three numeric codes at the firewall creation stage. The error codes do not identify the native rejection reason. The new run therefore confirms that changing only the IPv4 representation to CIDRs does not resolve the observed creation failure.

## Decision and limits

Do not replace the firewall expression in `deploy/qualification/windows-game.ps1` with the CIDR expression based on this evidence. Both representations were rejected by the hosted Windows Server 2022 firewall provider. The probe does not identify the rejected parameter or provider constraint, so it cannot support a narrower firewall change. The game's existing loopback exclusion, all-IPv6 block, and stop/account/service cleanup guards remain unchanged.

The probe's separate PowerShell 7.6.7 `-PureFixtureOnly` check passed earlier, confirming the CIDR list's mathematical equivalence to the original IPv4 ranges and refusal of malformed fixtures. That fixture does not exercise Windows firewall rule creation. The game harness still requires a successful hosted native run before its firewall behavior can be considered qualified.

## Follow-up isolation packet

The hosted provider-isolation run [39044359913](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39044359913) tested commit `c8c7e3c0ea7dcbb2ec698f760c0ee84546f610af`. Its retained summary is at `/home/thim/.cache/dilla-tmp/firewall-39044359913/windows-firewall-c8c7e3c0ea7dcbb2ec698f760c0ee84546f610af/firewall-summary.json`.

The read-only preflight found BFE and MpsSvc running, the firewall provider class and all three required NetSecurity commands available, and all fixed rule names absent. The `minimal_block`, `full_without_remote`, and `ipv4_ranges` cases created successfully and each was verified absent after cleanup. The IPv4 ranges case read back the exact normalized IPv4 set. The `ipv6_any`, `original`, and `cidr_equivalent` cases failed creation with the same bounded values: `HResult=-2146233088`, `NativeErrorCode=1`, `StatusCode=1`; each cleanup check passed. No raw provider error text was retained.

This narrows the current provider failure to cases containing an IPv6 address input. It does not show whether the provider rejects `::/0` specifically or all IPv6 input, and it does not identify the rejected parameter. The probe now adds `ipv6_split` with `::/1` plus `8000::/1`, and `ipv4_ranges_ipv6_split` with the original IPv4 ranges plus those two IPv6 prefixes. Its pure normalization fixture verifies that the IPv6 halves merge to exactly `::/0` with no gaps, and that the combined set equals the original ranges plus all IPv6 addresses. These new cases have not yet run on a hosted Windows runner.

The retained schema contains only fixed statuses and booleans for service/provider availability, case labels, booleans, numeric CIM codes, and finite failure categories. It does not retain exception messages or arbitrary fully qualified error IDs. The results remain diagnostic only; they do not justify a shipping firewall change.

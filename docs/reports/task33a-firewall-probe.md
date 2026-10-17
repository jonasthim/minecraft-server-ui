# Task 33a: Windows firewall address probe

## Observed failure

Windows game acceptance run [39042942229](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39042942229), job `117425924394`, stopped at the firewall stage on the existing rule creation call. The retained `result.json` has only numeric diagnostics: `HResult=-2146233088`, `NativeErrorCode=1`, and `StatusCode=1`, with source line 72. Those values do not identify which address input failed. No raw exception message was retained, so the cause remains unknown.

The existing call's `RemoteAddress` input contains two IPv4 ranges excluding `127.0.0.0/8`, plus `::/0`. Microsoft's [New-NetFirewallRule documentation](https://learn.microsoft.com/en-us/powershell/module/netsecurity/new-netfirewallrule?view=windowsserver2022-ps) lists both address ranges and subnets as accepted input formats. The docs do not establish whether this exact mixed IPv4-range/IPv6-subnet combination is accepted by the hosted Windows Server 2022 NetSecurity provider.

## Bounded experiment

Added `deploy/qualification/windows-firewall.ps1` as a disposable hosted Windows Server 2022 probe. It performs two independent create/readback/cleanup cases using fixed rule names and refuses either name if it exists before the experiment:

- `original`: the exact existing address set and rule fields from the game harness.
- `cidr_equivalent`: `128.0.0.0/1`, `0.0.0.0/2`, `64.0.0.0/3`, `96.0.0.0/4`, `112.0.0.0/5`, `120.0.0.0/6`, `124.0.0.0/7`, `126.0.0.0/8`, and `::/0`.

The CIDR set covers all IPv4 addresses except `127.0.0.0/8`, matching the two original IPv4 ranges, and retains all IPv6 addresses. Before running the native probe, a pure fixture normalizes ranges and CIDRs to merged numeric address intervals and verifies this equality.

On the hosted runner, each case uses `New-NetFirewallRule`, reads back the associated address filter, compares its normalized set, then removes the exact owned rule in a `finally` block and verifies it is absent before beginning the next case. It does not create an account, service, server process, client, or listener. The JSON report contains only a fixed case label, a creation boolean, numeric CIM codes, a normalized-address-set match boolean, and a cleanup boolean. The script has hosted-runner, Server 2022, administrator, preexisting-name, and private ordinary report-path checks.

The root integrator owns the minimal workflow wiring and hosted dispatch. The script writes its single `firewall-summary.json` result to the supplied existing empty `firewall-results` directory under `GITHUB_WORKSPACE`. This probe alone does not qualify the game harness or establish a shipping firewall fix. If the CIDR case succeeds and its returned normalized address set matches, that provides evidence for changing the game harness's address representation; the harness change still requires review and a fresh installed-game run.

## Local verification

- PowerShell 7.6.7 `-PureFixtureOnly` — passed. It exercised the mathematical range/CIDR equivalence and refused malformed path-like addresses, noncontiguous masks, out-of-range prefixes, reversed ranges, and scoped IPv6 input. It did not call Windows firewall cmdlets.
- PowerShell 7.6.7 parser — passed.
- `git diff --check -- deploy/qualification/windows-firewall.ps1 docs/reports/task33a-firewall-probe.md` — passed.

No local Windows or firewall native operation was run. The two cases have not yet been executed on a hosted Windows runner, so their create/readback/cleanup results remain pending.

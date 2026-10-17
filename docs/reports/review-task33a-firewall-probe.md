# Isolated Windows firewall experiment review

Integrator source review: PASS. This approves a disposable diagnostic experiment,
not a shipping firewall change or Minecraft qualification.

Two fixed names must be absent before the script creates rules for only
TCP25565/8080, inbound Block, all profiles. Original ranges and a CIDR partition
are tested separately. Created rules are read back, their address sets normalized,
then removed in finally; cleanup absence is checked even after partial cmdlet
failure. No server/listener/account/service is created. Only fixed case labels,
booleans and bounded-depth numeric exception codes are retained. Output uses
exclusive creation beneath the checked empty hosted workspace directory.

Root executed the pure PowerShell7.6.7 normalizer fixture successfully and
independently used Python ipaddress.address_exclude to prove the eight IPv4 CIDRs
equal the complement of127.0.0.0/8, covering exactly2^32−2^24addresses with no
loopback overlap. The IPv6 expression stays::/0. Readback normalization refuses
malformed masks/ranges. Root actionlint and diff checks passed.

The manual windows-firewall workflow has read-only repository permission, one
WindowsServer2022 job, ten-minute bound and bounded evidence retention. It builds
no game and is separately labelled. Actual syntax acceptance/readback/cleanup is
pending native execution; neither expression is accepted merely from source.
# Follow-up review: provider and parameter isolation

The first native run39043681496 completed with both address forms rejected and
both cleanup checks passing; its workflow success does not mean rule creation
succeeded. The follow-up adds six fixed block-rule cases and read-only BFE,
MpsSvc, provider-class and NetSecurity command availability. Every rule name is
prechecked, cleanup is verified, and any cleanup uncertainty halts later cases.
No shipping game restriction, firewall profile, service or account is changed.

Root corrected the availability check's initial Cmdlet-only assumption: CDXML
commands may be Functions. Exact command names and the NetSecurity module remain
required. The portable PowerShell7.6.7 pure fixtures pass, including Function,
Cmdlet, wrong-module and wrong-name cases. Numeric diagnostics and fixed category
outputs remain bounded; the new native probe is not yet executed.

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

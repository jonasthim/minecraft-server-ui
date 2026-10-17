# Locked test client dependencies

The isolated harness uses `npm ci --ignore-scripts`. No packages declare an install script. These dependencies are not part of the product build.

| Package | Exact version | License |
|---|---|---|
| @azure/msal-common | 14.16.1 | MIT |
| @azure/msal-node | 2.16.3 | MIT |
| @types/node | 26.6.5 | MIT |
| @types/node-rsa | 1.1.4 | MIT |
| @types/readable-stream | 4.0.25 | MIT |
| @xboxreplay/xboxlive-auth | 5.1.0 | Apache-2.0 |
| abort-controller | 3.0.0 | MIT |
| aes-js | 3.1.2 | MIT |
| ajv | 6.15.0 | MIT |
| asn1 | 0.2.3 | MIT |
| base64-js | 1.5.1 | MIT |
| buffer | 6.0.3 | MIT |
| buffer-equal | 1.0.1 | MIT |
| buffer-equal-constant-time | 1.0.1 | BSD-3-Clause |
| commander | 2.20.3 | MIT |
| debug | 4.4.3 | MIT |
| discontinuous-range | 1.0.0 | MIT |
| ecdsa-sig-formatter | 1.0.11 | Apache-2.0 |
| endian-toggle | 0.0.0 | MIT |
| event-target-shim | 5.0.1 | MIT |
| events | 3.3.0 | MIT |
| fast-deep-equal | 3.1.3 | MIT |
| fast-json-stable-stringify | 2.1.0 | MIT |
| ieee754 | 1.2.1 | BSD-3-Clause |
| json-schema-traverse | 0.4.1 | MIT |
| jsonwebtoken | 9.0.3 | MIT |
| jwa | 2.0.1 | MIT |
| jws | 4.0.1 | MIT |
| lodash.includes | 4.3.0 | MIT |
| lodash.isboolean | 3.0.3 | MIT |
| lodash.isinteger | 4.0.4 | MIT |
| lodash.isnumber | 3.0.3 | MIT |
| lodash.isplainobject | 4.0.6 | MIT |
| lodash.isstring | 4.0.1 | MIT |
| lodash.merge | 4.6.2 | MIT |
| lodash.once | 4.1.1 | MIT |
| lodash.reduce | 4.6.0 | MIT |
| macaddress | 0.5.4 | MIT |
| minecraft-data | 3.117.0 | MIT |
| minecraft-folder-path | 1.2.0 | MIT |
| minecraft-protocol | 1.68.0 | BSD-3-Clause |
| mojangson | 2.1.0 | MIT |
| moo | 0.5.3 | BSD-3-Clause |
| ms | 2.1.3 | MIT |
| nearley | 2.20.1 | MIT |
| node-fetch | 2.7.0 | MIT |
| node-rsa | 0.4.2 | MIT |
| prismarine-auth | 3.1.1 | MIT |
| prismarine-biome | 1.4.0 | MIT |
| prismarine-block | 1.23.0 | MIT |
| prismarine-chat | 1.13.0 | MIT |
| prismarine-item | 1.18.0 | MIT |
| prismarine-nbt | 2.8.0 | MIT |
| prismarine-realms | 1.6.0 | MIT |
| prismarine-registry | 1.12.0 | MIT |
| process | 0.11.10 | MIT |
| protodef | 1.19.0 | MIT |
| protodef-validator | 1.5.0 | MIT |
| punycode | 2.3.1 | MIT |
| railroad-diagrams | 1.0.0 | CC0-1.0 |
| randexp | 0.4.6 | MIT |
| readable-stream | 4.8.0 | MIT |
| ret | 0.1.15 | MIT |
| safe-buffer | 5.2.1 | MIT |
| semver | 7.8.5 | ISC |
| smart-buffer | 4.2.0 | MIT |
| string_decoder | 1.3.0 | MIT |
| tr46 | 0.0.3 | MIT |
| undici-types | 8.9.0 | MIT |
| uri-js | 4.4.1 | BSD-2-Clause |
| uuid | 11.1.1 | MIT |
| uuid-1345 | 1.0.2 | MIT |
| webidl-conversions | 3.0.1 | BSD-2-Clause |
| whatwg-url | 5.0.0 | MIT |
| yggdrasil | 1.8.0 | MIT |

The full upstream `minecraft-protocol` BSD notice is retained in `LICENSE.minecraft-protocol`. npm packages retain their own license files on installation.

`uuid` is deliberately overridden to exact `11.1.1` after integrator review; this exceeds the authentication parents’ declared ranges but preserves their CommonJS `v4()` callsites. Source tests verify resolution from both parents. See `docs/reports/task33a.md` for the advisory and audit evidence.

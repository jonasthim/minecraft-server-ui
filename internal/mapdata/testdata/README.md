# Task21 decoder fixtures

All `.nbt` files in this directory are **original synthetic data**, covered by
this repository's MIT license. They are not Minecraft-generated worlds and do
not qualify game versions, loaders, clients, or native operating systems.

`generate.py` writes independent byte-level NBT and bit-string packed arrays.
It never imports the Go decoder. The literal geometry is:

- x0/z0 has stone at localY0 and grass at localY5.
- x12/z0 has unknown block index16 at localY0: five-bit value16 crosses the
  first long boundary in dense packing and begins the second long in padded
  packing. Legacy uses block769, Add nibble3, metadata7.
- x13/z0 has an unknown block at localY0; legacy uses770, Add nibble3, metadata5.
- x15/z15 has water at localY15; other positions contain air.
- SectionY is0 for legacy/dense/padded fixtures and-4 for modern fixtures.

Go tests assert literal positions, Y values, labels and slice results. Test-only
NBT encoding supports focused corruptions and does not generate expectations.

`lz4-oracle.bin` contains synthetic1.20.5 NBT compressed by the independent
primary C `liblz4` implementation, wrapped with Java LZ4Block framing and the
primary C `libxxhash` checksum (lower28bits, seed0x9747b28c). It is not a standard
LZ4 frame. `xxhash-vectors.json` holds15 independent C oracle vectors including
empty/short/256-byte/18000-byte inputs and three seeds. Native libraries are
needed only to regenerate fixtures; checked-in fixtures make Go tests
network-free and native-library-free.

`PROVENANCE.json` records hashes, factual official artifact metadata and primary
format sources. Official executable artifacts were SHA1-checked and inspected
without running Minecraft; no executable bytes or artwork are distributed here.
`LZ4-LICENSE.txt` preserves the approved `github.com/pierrec/lz4/v4 v4.1.33`
BSD-3-Clause notice for downstream distribution. Original framing and XXH32 code
implement public format/algorithm descriptions; no upstream implementation was
copied.

A proposed additional read-only upstream fixture source is
[Tnze/go-mc, commit539b4a3](https://github.com/Tnze/go-mc/tree/539b4a3a7f030332eb58b8a946116ae7907630d2/save/testdata/region),
with its [MIT notice](https://github.com/Tnze/go-mc/blob/539b4a3a7f030332eb58b8a946116ae7907630d2/LICENSE)
(Copyright2019 Tnze). No upstream saved-world bytes are vendored: the inspected
repository did not establish independently known terrain geometry or original
game-generation provenance. A future fixture adoption should retain that notice,
source/hash and documented transformation and add an independent terrain oracle.

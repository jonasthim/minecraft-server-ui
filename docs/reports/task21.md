# Task21 — saved-world map decoding

2026-10-16. Submitted for integrator review; this report does not mark the package
accepted. The integrator owns acceptance review and `docs/IMPLEMENTATION.md`.
No staging, commits, game execution, EULA action, service install, vault write or
production host change was performed.

## Implemented consumer contract

`internal/mapdata` provides `New(discoverer, instance.Paths)`, `Summary(ctx,id)`
and `Tile(ctx,id,Request)`. The frozen handoff is
`.superpowers/sdd/2026-10-16-minecraft-server-ui/task21-contract.md`. Composition
and API authorization belong to Task22/root; this package has no HTTP routes,
shared schema, generated types or UI changes. Reuse one service for the process
so its two-reader admission bound applies to all map requests.

Summary exposes reviewed Task12 discovery plus only level.dat DataVersion,
Version.Name, source modification time, sampling time and consistency disclosure.
Dimension identity/path/layout come exclusively from Task12, including legacy
Vanilla layouts, Paper-associated roots, namespaced custom dimensions and26.1
standard folders. Unknown/ambiguous discovery remains explicit. Callers supply
an exact discovered dimension ID and chunk coordinates, never a source path.

A tile contains one16×16 saved chunk, cells in z*16+x order. Each cell reports
its highest non-air block at or below an optional inclusive maxY, world Y,
block label, original flat CSS color, known flag and empty flag. Unknown mod
blocks and unrecognized vanilla blocks use stable purple with their labels.
No Minecraft art or downloaded palette data is distributed. Legacy numeric
blocks retain Add nibbles and metadata; unresolved variants retain
`legacy:<id>:<metadata>` instead of being mislabelled as plain vanilla blocks.
Legacy1.12 mod registry limitations are disclosed.

States are ready/missing/stale/unsupported/corrupt/unknown. Failure states have
no cells or fingerprint. Ready means the saved read passed validation; it does
not assert live gameplay freshness. Provider/host errors and cancellation remain
errors; invalid identifiers/coordinates/heights are typed validation errors.
Unknown mod dimension identities are not silently treated as standard terrain.
The approved additive chunk_saved_at exposes the selected region-header save
timestamp (omitted for0), separately from sampled_at and containing-file mtime.
Its provenance is the game-written header and its clock may be wrong; it does
not prove live freshness. Task22 should label these timestamps separately.

## Format and safety behavior

- Bounded non-executing NBT: all standard tag kinds, big-endian numeric types,
  signed byte sectionY, Java modified UTF-8 including surrogate pairs, duplicate
  tag rejection, depth32,65536nodes,8MiB decompressed bytes, collection sizes
  checked against remaining bytes before large allocations.
- Legacy1343 sections: Blocks, Data and optional Add nibbles. Flattened disk
  palettes use minimum4bits and schema-derived width; pre1.16 dense values can
  span longs,1.16 padded values cannot. Modern root sections/block_states,
  negative Y, singleton palettes and valid light-only sections are supported.
  Palette indices and entire arrays are validated even below the displayed
  surface. Nonstandard declared bit-width tags are refused rather than guessed.
- DataVersion routing is per chunk, with schema validation:1343;1519–2230;
  2566–2730;2860–5023. Snapshot transition gaps and versions outside those bounds
  explicitly refuse parsing. These ranges select reader schemas and do not
  establish support for every intervening release or mod format.
- Region names derive solely from bounded integer coordinates. All1024sector
  locations must be valid, non-overlapping and within the padded region file.
  Header sectors, zero sector counts, truncated tables/records and invalid
  lengths are rejected. Floor division handles negative region coordinates.
- Compression is selected by each chunk record: gzip/zlib/raw/Java LZ4Block.
  Mixed codec and old/new DataVersion chunks in the same region are tested.
  External high-bit records accept only their exact derived c.x.z.mcc file;
  malformed stubs, missing committed external data and links cannot fabricate
  terrain. Custom codec127 is explicitly unsupported.
- Linux files use rooted O_NOFOLLOW/O_NONBLOCK opens and single-link regular-file
  checks. Windows checks reparse attributes and link counts. Parent directories
  are opened one component at a time through identity-checked os.Root handles;
  final rechecks detect replaced directories and files. Unqualified other host
  platforms refuse saved-file access.
- Region header, selected record, external chunk and level.dat bytes are reread
  after decoding, alongside identity/size/mtime checks. Same-size writes with a
  restored timestamp are detected by the content pass. Observed changes yield
  stale with no cells. These checks cannot guarantee cross-file/cross-tile
  point-in-time coherence or detect every concurrent adversarial ABA change.
- Requests have a10second deadline, at most two concurrent reads per service,
  at most64sections, chunk coordinates[-1875000,1874999], Y[-2032,2031], region
  size<=1GiB, external/metadata compressed size<=16MiB, decoded NBT<=8MiB.
  Context passes through reads, decompression, parsing and rendering.
- No persistent or memory tile cache is introduced. Fingerprints bind policy,
  instance/dimension, coordinates/slice and source identity/mtime/size/content.
  Consumer caches must remain bounded and revalidate on the next sample. The
  source modification time is containing-file age evidence and can advance when
  another chunk is saved; it never proves selected terrain is fresh.

The approved dependency is `github.com/pierrec/lz4/v4 v4.1.33` (BSD-3-Clause,
no transitive modules). Root owns go.mod/go.sum. Only fixed-destination
UncompressBlock is used; Java framing bounds and checksum checks are owned here.
The original seededXXH32 implementation is checked against15 primary C vectors;
Java asChecksum's lower28bit mask is verified from its primary implementation.
The dependency notice is retained in
`internal/mapdata/testdata/LZ4-LICENSE.txt` for release distribution.

## Acceptance evidence and limits

Original synthetic serialized fixtures cover1.12.2 legacy/Add,1.13 and1.15
dense packing,1.16 padded five-bit long boundaries,1.18 negative Y,1.20.5 modern
mixed codecs,1.21.9 and26.1/26.3 modern schemas. The Python generator uses
independent bit-string construction, while Go tests assert literal expected
block names, positions and Y values. There is no shared decoder/expectation
algorithm. C liblz4 generates the independent compressed fixture and C libxxhash
generates the checksum vectors. All fixtures are explicitly synthetic, original
and MIT-licensed; tests require neither a network nor those native libraries.
Hashes and source/license metadata live in testdata/PROVENANCE.json and README.

Tests include malformed/truncated NBT, invalid modified UTF, deep/list/byte
bombs, decompression bombs, trailing compression bytes, invalid palette indices,
wrong types, duplicate/out-of-range Y, oversized sections, incomplete modern
chunks, unknown DataVersions, coordinate mismatch, malformed sector tables,
sector overlap, external chunks, symlink/hardlink rejection, directory
replacement, same-time same-size mutation, stable and invalidated fingerprints,
all service states, negative chunk coordinates, private-NBT non-disclosure,
bounded concurrency and admission release. Source safety uses real temporary
filesystem objects. The discovery service used by service tests is the real
Task12 service; only its instance configuration store is a small test double.

RED evidence observed before implementation: missing parser/types/decoder/codec/
source/region/service symbols failed compilation. After initial readers existed,
light-only modern sections failed, zlib trailing bytes were incorrectly accepted,
and nonzero legacy metadata was mislabelled as plain stone. Focused tests failed
for each of those behaviors before the corresponding correction.

Verification from this worker:

- `go test ./internal/mapdata -count=1`: PASS,0.057s at final implementation check.
- `go test -race ./internal/mapdata -count=1`: PASS,1.591s.
- `go vet ./internal/mapdata`: PASS.
- `GOOS=windows GOARCH=amd64 go test -c ./internal/mapdata -o /home/thim/.cache/dilla-tmp/mapdata-task21.test.exe`: PASS; cross-compilation only.
- `go test ./internal/mapdata -run '^$' -fuzz '^FuzzChunkDecoder$' -fuzztime=5s -parallel=2`: PASS,12executions and5baseline seeds. This is a short smoke, not extensive fuzz qualification.
- `git diff --check`: PASS in the shared working tree at this check.

Broad suites were intentionally left to integration while peers edit shared
packages. A fixture-generation environment probe `dpkg-query` was unavailable;
it did not affect Go tests or fixture generation. Generated oracle bytes are
checked in; no native-library version claim is made from that missing probe.

No actual game save, client connection or native Windows filesystem execution
was qualified.1.21.9 MSMP and26.3 log changes are outside terrain decoding; their
presence in synthetic layout/schema tests does not qualify those protocols.
Stable release and loader support claims remain pending the required real
server/client/platform matrix. Task22 must still validate role checks, five
second sampling, cache presentation, browser/mobile accessibility and composition.

## Primary source research

Read without running game code:

- [Mojang24w04a](https://feedback.minecraft.net/hc/en-us/articles/23489515647117-Minecraft-Java-Edition-Snapshot-24w04a): compression selection is per newly written chunk; changing compression does not rewrite all existing data.
- [Mojang version manifest](https://piston-meta.mojang.com/mc/game/version_manifest_v2.json) and SHA1-verified official client version.json metadata:1.21.9 world_version4554,26.1=4786,26.3=5023. Client SHA1s respectively ce92fd8d1b2460c41ceda07ae7b3fe863a80d045,191771837687b766537a8c4607cb6fad79c533a1,e877b6a07acd633fb3bb475002175cec036e7b87. Artifacts were read into memory and discarded; no executable bytes are vendored.
- Cached official26.3 server33680f5f2ac32864d6d7cf5e56a705fdb3e05f4c was inspected via javap: SerializableChunkData/RegionFile/RegionFileVersion/SimpleBitStorage establish modern sections, signed Y, block_states, padded values, codec IDs and external high-bit framing. No Minecraft classes were executed or copied.
- [Primary Java LZ4Block framing](https://github.com/lz4/lz4-java/blob/1.8.0/src/java/net/jpountz/lz4/LZ4BlockInputStream.java), [checksum adapter](https://github.com/lz4/lz4-java/blob/1.8.0/src/java/net/jpountz/xxhash/StreamingXXHash32.java), [LZ4 block specification](https://github.com/lz4/lz4/blob/v1.10.0/doc/lz4_Block_format.md), [XXH32 specification](https://github.com/Cyan4973/xxHash/blob/v0.8.3/doc/xxhash_spec.md).
- [Prismarine dense implementation](https://github.com/PrismarineJS/prismarine-chunk/blob/master/src/pc/common/BitArray.js), [padded implementation](https://github.com/PrismarineJS/prismarine-chunk/blob/master/src/pc/common/BitArrayNoSpan.js), [Anvil serializer](https://github.com/PrismarineJS/prismarine-provider-anvil/blob/master/src/1.13/chunk.js), and [go-mc saved schema](https://github.com/Tnze/go-mc/blob/539b4a3a7f030332eb58b8a946116ae7907630d2/save/chunk.go): primary implementation cross-checks, not copied code or game qualification.
- [Approved dependency source/license](https://github.com/pierrec/lz4/tree/v4.1.33).

One safe proposed future read-only upstream fixture source is
[Tnze/go-mc save/testdata/region](https://github.com/Tnze/go-mc/tree/539b4a3a7f030332eb58b8a946116ae7907630d2/save/testdata/region),
pinned539b4a3a7f030332eb58b8a946116ae7907630d2, under
[MIT Copyright2019 Tnze](https://github.com/Tnze/go-mc/blob/539b4a3a7f030332eb58b8a946116ae7907630d2/LICENSE).
Root approved bounded inspection. It was not vendored because an independently
known terrain oracle and game-generation provenance were not established; a
mere decoder-success smoke would not add the required evidence.

## Independent review fix round1 — reviewed base a4ff30d

The independent review found two Important gaps and one Minor error-reporting
defect. This bounded revision addresses all three and awaits integrator re-review.
The public consumer contract, shared instance path helpers, schema, UI and
module dependencies were not changed. No staging or commits were performed.

1. **Ancestor substitution:** final-path Lstat/OpenRoot identity checks could
   accept an already-substituted ancestor symlink and then read an outside server
   directory with every final identity check passing. `openSource` now opens the
   filesystem root (or a local Windows drive root), then descends every absolute
   component using the existing rooted directory checks. The complete chain of
   handles/identities remains open through the final source verification. UNC,
   device and extended-volume Windows forms are explicitly unsupported until a
   separate trusted-anchor policy exists. Paths still derive from instance.Paths
   and Task12 discovery; no second layout or adoption policy was added.

   `TestSourceRejectsAncestorSwapAfterInstancePathValidation` deterministically
   calls Paths.For, renames the validated instance ancestor, substitutes a symlink
   to an outside/server directory and invokes the real opener. RED before the fix:
   `ancestor substitution admitted an outside server directory`. It now refuses
   the traversal. `TestSourceRetainsAndRechecksAncestorAfterOpen` swaps an ancestor
   after acquisition, proves reads stay on the original inside descriptor and
   requires stale when the retained chain is rechecked.

2. **Exact Add and packing evidence:** the initial generic unknown-block assertion
   did not prove Add nibble decoding. The independent fixture test now requires
   the exact labels `legacy:769:7` and `legacy:770:5`, testing the low/high Add and
   metadata nibbles. Dense and padded fixtures require `example:boundary` and
   `example:ore` at their literal expected positions/Y. Two temporary Go overlays
   verified these assertions can fail without modifying production files: removing
   Add decoding failed with actual `legacy:1:7` versus expected `legacy:769:7`;
   removing dense cross-long carry failed with air versus `example:boundary`.
   The original production algorithms needed no correction for this finding.

3. **Host I/O errors:** ReadAt formerly replaced every failure with stale, losing
   causes such as a closed descriptor/device error. Reads now wrap the original
   host error; EOF/UnexpectedEOF against the captured size is classified as
   observed truncation/stale while retaining its cause. Directory and file
   identity rechecks also preserve stat/lookup host errors; disappearance of a
   previously pinned object remains stale. Real closed-handle regressions verify
   errors.Is(os.ErrClosed), absence of errStale for host failures and retention
   through the consumer status conversion. Before the fix the read and verification
   regressions both reported stale instead of retaining os.ErrClosed.

Fresh scoped verification after the implementation corrections:

- `go test ./internal/mapdata -count=1`: PASS,0.051s.
- `go test -race ./internal/mapdata -count=1`: PASS,1.593s.
- `go vet ./internal/mapdata`: PASS.
- `GOOS=windows GOARCH=amd64 go test -c ./internal/mapdata -o /home/thim/.cache/dilla-tmp/mapdata-task21-review1.test.exe`: PASS; cross-compilation only.
- `git diff --check`: PASS at the final implementation check.

Native Windows traversal/reparse/volume execution and real game qualification
remain pending. No broad suites or additional fuzz qualification were run in
this fix round. All implementation changes are confined to source.go,
source_test.go and chunk_test.go under internal/mapdata; this report is the only
other owned change.

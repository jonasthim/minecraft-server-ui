# Task29 — release candidate producer and authenticated artifact contract

Date: 2026-10-17. Implementation ready for integrator review. This report does not
accept the work package or claim native installed/game qualification. The
integrator owns the ledger and build/workflow wiring.

## Implemented boundary

New owned files are `release/candidate/**`, `release/cmd/package/**`,
`release/cmd/verify/**`, `release/CANDIDATES.md`, and this report. Existing
qualification gate/evidence, notices generator/inventory/licenses and trust were
preserved. No dependencies were added; production producer/bootstrap code imports
only Go standard library packages. The separate artifact checker imports the
existing shipping self-update package as required.

The producer accepts four stamped manager/updater ELF/PE inputs and their native
version receipts, repository/tag/schema/time, fixed checked deployment assets,
notices, and supplied signing material. It emits deterministic manager update
archives with the existing unchanged-schema signed manifest and separate signed
bootstrap bundles. Archive members, filenames, bounds, modes and ordering are
frozen in [the contract](../../release/CANDIDATES.md).

The update archives contain exactly manager + `LICENSE` +
`THIRD_PARTY_NOTICES.txt`. Bootstrap archives contain exactly fourteen Linux or
five Windows members covering both executables and all existing privileged
installer/helper/unit/config inputs. No arbitrary source/archive collection or
member extraction is exposed by the producer or bootstrap verifier.

Inputs and parent symlinks, nonregular/empty/oversized sources, wrong Go command,
wrong ELF/PE target and receipt version/hash/schema mismatch fail before output
publication. Private snapshots prevent later source changes from altering the
packaged bytes. Fixed archive metadata avoids source modes/timestamps in output.
Existing output paths are refused; publication errors remove the newly owned
output directory and return cleanup errors. Consumers wait for successful command
exit before reading the candidate directory. The caller must own/trust its parent.

## Signing, receipts and initial trust

Ed25519 uses the standard library. Owner signing input is a private file or private
standard input, never an argument/environment secret. Unix key permissions and
symlinks are checked; Windows owners must provide private ACLs. Invalid-input
errors do not contain signing material. No production key is generated. The
explicit development mode makes only an ephemeral in-memory key and public trust
configuration, labels metadata/artifacts `DEVELOPMENT ONLY` and publishes no
private key. Owner-signed provenance does not authorize stable publication.

A concrete Go1.27 finding changed the initial version-proof proposal: `-trimpath`
removes linker flags from build information. Native CI therefore runs the exact
built `version` commands and manager `update-capability`, then writes
`<binary>.version.json`, binding OS/architecture/command/version/schema to SHA256.
The producer verifies those receipts and executable build/format identity without
executing inputs. These unsigned receipts are CI provenance; their native command
logs provide the actual evidence. Package-test cross compilation proves format
handling only. Root owns the real updater version command and receipt workflow.

First-install authentication is explicitly unresolved by downloading a verifier
from the same source. The approved Task30 handoff uses an operator-reviewed,
release-specific outer script with an out-of-band approved exact bootstrap
manifest digest. Shell/PowerShell use built-in SHA256 facilities; no handwritten
Ed25519 implementation is added. An already trusted Go consumer may use
`candidate.VerifyBootstrap` and `candidate.VerifyBundle`. Existing reviewed trust
must be preserved; the empty example trust asset remains empty.

## Verification evidence

The scoped command `go test -count=1 ./release/...` passed all three new packages.
`go vet ./release/...` passed. `go test -race -count=1 ./release/...` passed (`candidate` 24.050s,
`cmd/package` 1.006s, `cmd/verify` 1.013s). `gofmt -l release/candidate
release/cmd` returned no files.

Meaningful exercised behavior includes:

- Shipping `selfupdate.VerifyManifest` and `selfupdate.Stager` accept both actual
  producer-created update archives and preserve their manager bytes.
- Repeating the producer with the same supplied fixture key/time/inputs produces
  byte-identical output, including after source permission/timestamp changes.
- Real CLI owner signing through private standard input and ephemeral development
  signing both produce the expected eight/ten outputs. The artifact-check CLI
  accepts their complete produced bytes and rejects modified update/bootstrap
  files. No input executable is executed by these two commands.
- Bootstrap signature/tamper/untrusted-key, repository/tag/purpose/schema/expiry,
  exact-platform/member/mode and member-hash checks fail closed. Hash-adjusted
  malicious archives with extra, traversal, duplicate, symlink, oversized or
  appended members/payloads are rejected, rather than only failing the outer hash.
- Signed update archive rejection uses genuine original manager/license bytes
  plus injected unsafe members, so the tests reach shipping archive handling.
- A focused red/green regression demonstrated that a ZIP local-header name could
  disagree with its authenticated central name. The bootstrap verifier now checks
  both local name and local flags/method, contiguous payload offsets, descriptors,
  bounded exact-count central directory and no hidden/trailing local data.
- Missing/mismatched/duplicate/unknown native receipts, unsafe/symlinked/oversized
  sources, version overflow, output clobbering, cancellation and private-input
  secrecy/permissions are covered.

No broad application suite was run for this isolated producer task; the integrator
requested scoped verification. Fixture executables are deliberately small Go
programs, not the shipping application or a game runtime.

## Exact CI artifact verification command

Root owns the native-build and packaging workflow. After successful production of
its actual candidate bytes, run:

```sh
go run ./release/cmd/verify \
  --artifacts candidate-output \
  --trust-file candidate-output/development-trust.json \
  --repository jonasthim/minecraft-server-ui \
  --version "$CANDIDATE_VERSION" --schema "$candidate_schema"
```

The checker runs shipping manifest verification and staging for both platforms in
private temporary roots, validates both bootstrap bundles, and compares the two
signed documents' schema/time/manager/license identities. It removes temporary
stages/spools, executes no inputs, and installs no service or trust. It permits
root-added source/evidence reports beside the fixed authenticated assets. With
public development trust it verifies fixture consistency, not production identity.

## Pending evidence and release gates

The actual four native application binaries/receipts and their candidate workflow
packet have not been supplied to this worker. The root's workflow must retain and
verify those exact bytes through the command above. Do not substitute the passing
fixture tests for this packet or native installed tests. Native installed
installation/upgrade/failed-health rollback, target OS/version coverage, real
server/client/restore matrix, production key custody/bootstrap trust and stable
publication remain separate integrator/release-owner gates. No service, account,
world, production trust or release was changed by this task.

## Integrator native candidate acceptance

Independent review of519ebe1 passed specification and quality with zero findings.
[Run39028355162](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39028355162)
atc863c63866de3d2ae0626069cc18b13ddf5d8ec3 then passed both native build/probe jobs
and the packaging/actual-file verification job. Linux and Windows each executed
the genuine manager and updater version commands forv0.0.1; manager capability
probes bound the current schema. The producer consumed those native bytes and
receipts; the shipping checker accepted both update stages and both bootstrap
bundles. No input executable was run by the packaging job.

Root reviewed the successful probe, verifier and artifact-retention logs. Input
artifacts12098524347 and12099551145 are retained for seven days; development
candidate artifact12099477276 is retained for fourteen days (44,802,688 bytes,
ZIP SHA2567307f161d0a37c1a3bc2b59f283fe767d02582e049d950f808398c61ca7bd49f).
This closes Task29's native candidate-production acceptance. All trust remains
ephemeral fixture/public-key evidence; installation, update recovery, games,
production publisher identity and stable release qualification remain separate.

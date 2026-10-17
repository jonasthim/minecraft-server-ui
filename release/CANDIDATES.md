# Release candidate artifact contract, format 1

Task29 produces local candidates. Stable publication, native installed qualification,
production trust and signing key custody remain separate release-owner gates.
`release/check_qualification.py`, its evidence and current trust configuration are
unchanged by the producer.

## Build inputs and native version receipts

Build the manager and updater on each actual target runner, with the embedded UI
and notices already checked. Keep `-trimpath`; stamp both commands, for example:

```sh
go build -trimpath -ldflags '-s -w -X main.version=v0.0.1' -o bin/minecraft-ui-linux-amd64 ./cmd/minecraft-ui
go build -trimpath -ldflags '-s -w -X main.version=v0.0.1' -o bin/minecraft-ui-update-linux-amd64 ./cmd/minecraft-ui-update
```

The corresponding Windows commands use `.exe` output paths. Execute each binary's
`version` command on its native runner and require the exact requested version.
Also execute the manager's read-only `update-capability` command and require its
version/schema. Record a receipt **only after these checks pass** beside each
binary, as `<binary path>.version.json`:

```json
{"format":1,"os":"linux","arch":"amd64","command":"minecraft-ui","version":"v0.0.1","binary_sha256":"<64 lowercase hexadecimal characters>","schema":18}
```

The four receipts have these exact fields. `os` is `linux` or `windows`, `arch` is
`amd64`, `command` is `minecraft-ui` or `minecraft-ui-update`; updater receipts omit
`schema` (or set it to zero), and manager receipts require the positive actual compiled schema.
Whitespace and field order are free. Unknown/duplicate keys and files over 4096
bytes are refused. Hash the exact binary bytes after native execution.

Go1.27 omits `-ldflags` from build information when `-trimpath` is used. The producer
therefore verifies Go command identity, GOOS/GOARCH and ELF/PE executable identity,
then binds the owner's native version receipt to the snapshotted binary SHA256,
requested version and schema. It never executes supplied binaries. Receipts are
unsigned build provenance: a supplied receipt alone cannot prove a native test
happened. The CI run and its native command logs are the evidence for that claim.
Cross compilation in package tests establishes format handling only.

## Producer command

The output parent directory must exist; `--out` itself must not exist. The caller
must use an ordinary trusted parent directory and consume output only after the
command exits successfully. An existing output path is never overwritten. Inputs
are privately snapshotted and validated before publication. A publication error
removes the newly created output directory and reports cleanup errors; readers
must not watch an in-progress directory as a completed release transaction.

```sh
go run ./release/cmd/package \
  --source . \
  --linux-manager bin/minecraft-ui-linux-amd64 \
  --windows-manager bin/minecraft-ui-windows-amd64.exe \
  --linux-updater bin/minecraft-ui-update-linux-amd64 \
  --windows-updater bin/minecraft-ui-update-windows-amd64.exe \
  --version v0.0.1 --repository jonasthim/minecraft-server-ui --schema 18 \
  --published-at 2026-10-17T12:00:00Z \
  --out artifacts/candidate --development
```

The publication time is an explicit whole-second UTC RFC3339 timestamp. Both signed
metadata documents expire exactly 30 days later. Stable-format tags are required
by the existing updater; a `v0.0.1` candidate tag does **not** imply stable approval.

`--development` creates a disposable Ed25519 key in memory, labels bootstrap
metadata `DEVELOPMENT ONLY`, and writes only its public verification configuration
in `development-trust.json`. Its private key is never written or logged. These
candidates cannot be recreated with the same signature after that key is gone.
The library's tests supply the same disposable key to verify reproducibility.

An authorized owner instead supplies `--key-id <reviewed id>` and
`--signing-key-file <private file>`; `--signing-key-file -` reads private standard
input from an owner-controlled pipe. These modes are mutually exclusive with
`--development`. Key input is a raw 32-byte Ed25519 seed, a valid raw 64-byte
Ed25519 private key, or strict base64 of either. Input is bounded to 1024 bytes.
File and parent symlinks are refused. Unix files must deny group/other access;
Windows owners must provision private ACLs because Go FileMode does not describe
Windows ACLs. Never put signing material in arguments, environment variables,
logs or artifacts. The tool generates no production key and configures no trust.

Given the same four binary bytes, receipts, assets, signing key/key ID, identity,
schema and publication time, all output bytes are identical. Source paths, modes,
file modification times, working directory and archive creation time do not enter
the output. Archive ordering and modes are fixed. Producer code uses only the Go
standard library and adds no dependencies.

## Output filenames and shipping manager updates

Every successful output contains these eight files:

| File | Purpose |
| --- | --- |
| `release.json` | Existing format1 `selfupdate.Manifest`, unchanged schema before/after |
| `release.sig` | Existing Ed25519 signature envelope |
| `minecraft-ui-linux-amd64.tar.gz` | Linux update archive, exactly three files |
| `minecraft-ui-windows-amd64.zip` | Windows update archive, exactly three files |
| `bootstrap.json` | Separate format1 bootstrap document below |
| `bootstrap.sig` | Ed25519 signature over exact bootstrap document bytes |
| `minecraft-ui-bootstrap-linux-amd64.tar.gz` | Complete Linux installation inputs |
| `minecraft-ui-bootstrap-windows-amd64.zip` | Complete Windows installation inputs |

Development mode adds `DEVELOPMENT-ONLY.txt` and `development-trust.json` (public
key only). Neither output contains a private key, account credential or shared
provider credential. Owner-signed bootstrap metadata uses the provenance string
`OWNER SIGNED`; that string grants no qualification or publication authorization.

Update archives contain exactly `minecraft-ui` (Linux) or `minecraft-ui.exe`
(Windows), `LICENSE`, and `THIRD_PARTY_NOTICES.txt`. They contain no updater,
installer, service unit, helper or configuration. `release.json` has exactly Linux
and Windows amd64 artifacts and the existing archive/binary hashes and sizes.
Tests use the shipping `selfupdate.VerifyManifest` **and** `selfupdate.Stager` for
both produced update archives. Never weaken those checks to accommodate bundles.

## Separate authenticated bootstrap metadata

`bootstrap.json` is compact UTF-8 Go `encoding/json` output in this field order,
followed by one newline. No duplicate/unknown fields, trailing JSON or alternative
whitespace are allowed by the Go verifier. The example is expanded for reading:

```json
{
  "format": 1,
  "purpose": "minecraft-ui-bootstrap",
  "repository": "jonasthim/minecraft-server-ui",
  "version": "v0.0.1",
  "published_at": "2026-10-17T12:00:00Z",
  "expires_at": "2026-11-16T12:00:00Z",
  "schema": 18,
  "trust": "DEVELOPMENT ONLY",
  "artifacts": [
    {"os":"linux","arch":"amd64","name":"minecraft-ui-bootstrap-linux-amd64.tar.gz","size":123,"sha256":"<hex>","members":[
      {"name":"minecraft-ui","size":123,"sha256":"<hex>","mode":493}
    ]},
    {"os":"windows","arch":"amd64","name":"minecraft-ui-bootstrap-windows-amd64.zip","size":123,"sha256":"<hex>","members":[]}
  ]
}
```

Both artifacts and every member are mandatory; the abbreviated example is not a
valid manifest. `size` is the exact byte length; `sha256` is lowercase hex SHA256;
`mode` is an integer Unix permission mode, shown in octal in the tables below.
`bootstrap.sig` uses the same `{key_id,signature}` envelope as `release.sig`, in
canonical compact JSON plus newline. The signature is standard base64 Ed25519
of the **exact document bytes**, including that newline. Public keys always come
from independently reviewed native configuration or an owner-approved bootstrap
anchor, never from the signed document itself.

The two artifact entries are ordered Linux then Windows. Members are flat,
ordered and exact, with no directory, links, hard links, alternate streams,
traversal, duplicate, extra or special entries. Files come from the four binary
inputs, the repository root notices, and these fixed deployment paths.

Linux has exactly fourteen members:

| Order | Member | Source | Mode |
| --- | --- | --- | --- |
| 1 | `minecraft-ui` | Linux manager input | `0755` |
| 2 | `selfupdate` | Linux updater input | `0755` |
| 3 | `LICENSE` | `LICENSE` | `0644` |
| 4 | `THIRD_PARTY_NOTICES.txt` | `THIRD_PARTY_NOTICES.txt` | `0644` |
| 5 | `install.sh` | `deploy/linux/install.sh` | `0755` |
| 6 | `unitctl` | `deploy/linux/unitctl` | `0755` |
| 7 | `minecraft@.service` | `deploy/linux/minecraft@.service` | `0644` |
| 8 | `minecraft-ui.service` | `deploy/linux/minecraft-ui.service` | `0644` |
| 9 | `sudoers` | `deploy/linux/sudoers` | `0440` |
| 10 | `minecraft-ui-update@.service` | `deploy/linux/minecraft-ui-update@.service` | `0644` |
| 11 | `minecraft-ui-update-recovery.service` | `deploy/linux/minecraft-ui-update-recovery.service` | `0644` |
| 12 | `minecraft-ui-update.conf` | `deploy/linux/minecraft-ui-update.conf` | `0644` |
| 13 | `update-trust.example.json` | `deploy/linux/update-trust.example.json` | `0644` |
| 14 | `NOTICE` | `deploy/linux/NOTICE` | `0644` |

Windows has exactly five members:

| Order | Member | Source | Mode |
| --- | --- | --- | --- |
| 1 | `minecraft-ui.exe` | Windows manager input | `0755` |
| 2 | `selfupdate.exe` | Windows updater input | `0755` |
| 3 | `LICENSE` | `LICENSE` | `0644` |
| 4 | `THIRD_PARTY_NOTICES.txt` | `THIRD_PARTY_NOTICES.txt` | `0644` |
| 5 | `install.ps1` | `deploy/windows/install.ps1` | `0644` |

The trust example remains empty; it must never replace existing reviewed trust.
Qualification scripts and reports are not privileged installation inputs and are
excluded. If Task30 adds a privileged installer/helper/unit/config asset, update
and review this exact allowlist and its tests before packaging it. No wildcard
source collection is permitted.

Each binary is at most 256MiB, each notice/deployment asset at most 1MiB, bootstrap
metadata at most 32KiB, and a bootstrap bundle at most 528MiB. Update archive limits
remain the shipping 256MiB bound. Sources and their parents must be ordinary
non-symlink paths; files must be regular and nonempty. ZIP uses the exact bounded
non-ZIP64 directory with DEFLATE entries and no comments/extra fields/hidden local
payload. Linux uses regular USTAR entries in one gzip stream, with bounded zero
padding and no appended stream/payload. Archive creation has fixed metadata.

## Task30 dependency-free consumer handoff and first trust

A downloaded executable cannot authenticate itself. Downloading a verifier,
manifest and public key from the same untrusted location does not establish an
initial publisher identity. Owner signatures alone do not solve first install.

The approved initial route is an operator-reviewed **release-specific bootstrap
script** containing an out-of-band approved SHA256 of `bootstrap.json` and fixed
repository/version identity. Linux can compute SHA256 with the installed system
SHA256 utility; Windows PowerShell can use its built-in .NET SHA256 implementation.
No Ed25519 implementation is copied into shell or PowerShell. A future already
trusted verifier can instead use the Go standard library implementation here.

Task30's pinned script must bound the metadata download, compare its **exact bytes**
to the embedded digest before interpreting it, validate identity/validity/schema
policy and the exact platform allowlist, download only the fixed bundle name, and
check its full size/hash before any archive operation. It must validate member
names/types/counts/sizes/hashes in a private staging directory and activate only
those fixed privileged destinations. Avoid general extraction of unvalidated
paths. Preserve existing data/config/trust and follow the native installer
activation/recovery policy; successful artifact verification does not authorize
a service mutation by itself. A wrapper cannot contain its own final document's
digest if it is included in that document: the reviewed release-specific outer
bootstrap script is a separate trust anchor, not a self-authenticating member.

For an already trusted Go consumer:

```go
m, err := candidate.VerifyBootstrap(trust, body, signature, requestedVersion, now)
// Enforce desired schema/installation policy; select exactly the expected OS.
err = candidate.VerifyBundle(ctx, m.Artifacts[platformIndex], downloadedBundle)
```

`VerifyBundle` creates only a bounded private temporary spool, validates the whole
bundle and exact ordered member allowlist, then removes the spool. It never
extracts member paths or installs anything. Reverify on privilege crossing or
retain an immutable checked source handle; do not verify a path, release it to
an unprivileged writer, and later execute it as administrator.

## Verification and remaining gates

```sh
go test -race -count=1 ./release/...
go vet ./release/...
```

After producing the actual CI candidate, verify those exact files as well:

```sh
go run ./release/cmd/verify --artifacts artifacts/candidate \
  --trust-file artifacts/candidate/development-trust.json \
  --repository jonasthim/minecraft-server-ui --version v0.0.1 --schema 18
```

Use the schema recorded by the actual native manager receipt; 18 is the current
repository schema in these examples. The checker verifies `release.json` through
the shipping verifier, stages both exact update archives in private temporary
roots through the shipping Stager, then checks both bootstrap bundles through the
bootstrap verifier. It also binds bootstrap/update schema, validity, manager bytes
and notice bytes together. It executes no input binaries and installs no service
or trust. An explicit `--now` is available for fixture-only time tests; normal CI
uses current time. A public development trust file proves fixture consistency;
it does not establish production publisher identity. Owner checks instead pass
an independently reviewed trust file. Temporary stages/spools are removed.

Package tests cover deterministic outputs, real Go executable formats, shipping
verification/staging for both update archives, bootstrap authentication and exact
member validation, signature/hash/identity/expiry failures, signed malicious
archives, receipt mismatch, unsafe source refusal, non-clobbering output and
private input handling. The fixture binaries are deliberately small and contain
no application or game runtime. CI must additionally retain actual native manager
and updater inputs/receipts, produced candidates and native command logs. Installed
upgrade/rollback, real game/client/restore matrix, Windows versions beyond the
specific tested runner, production custody/trust and stable publication remain
separate gates in the implementation ledger.

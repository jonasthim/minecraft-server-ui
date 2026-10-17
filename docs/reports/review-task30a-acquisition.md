# Task30A1 acquisition independent review

Reviewed frozen candidate `72ea5bb30bb449807a9f57af6506d4d42e429848`
against base `d0994d1`, limited to its twelve committed files. Reviewed the
approved design, original and corrective plans, implementation ledger,
`release/CANDIDATES.md`, frozen acquisition brief/diff, and submitted evidence
report. Concurrent qualification and outer-installer files are excluded.

**Specification: FAIL pending R1. Quality: request changes.** One Important
finding; no other material finding identified in the scoped review.

## R1 — Important: snapshot replacement is accepted and then deleted

Location: `release/bootstrap/prepare.go:199` and
`release/bootstrap/prepare.go:213`; creation identity is recorded at
`release/bootstrap/files.go:189`.

After copying the archive, preparation closes the file originally created as
`.bundle`, then reopens that name through `openRegular`. This verifies the
replacement's own opened identity, but never compares it with the identity
recorded when `.bundle` was exclusively created. Successful preparation later
unconditionally removes that name. A distinct replacement containing identical
authenticated bytes is therefore accepted and deleted, although it was not
created by this operation.

Independent deterministic reproduction on an isolated `git archive 72ea5bb`
copy: reuse `newFixture` and supply an `Acquisition` wrapping its archive reader.
In the returned reader's `Close` callback, remove `Output/.bundle` and create a
distinct regular file at that name containing the same fixture archive bytes.
This callback runs at `prepare.go:195`, before the subsequent reopen and
verification. Assert that `Prepare` refuses and that the replacement remains.
The probe fails on the candidate:

```text
Prepare error=<nil>; replacement .bundle stat=lstat .../prepared/.bundle:
no such file or directory (want refusal and preserved replacement)
```

The isolated probe is retained at
`/home/thim/.cache/dilla-tmp/task30a-review-72ea5bb/release/bootstrap/review_probe_test.go`.
Run it there with:

```sh
go test -run '^TestReviewSnapshotSubstitution$' -count=1 ./release/bootstrap
```

This requires a writer able to modify the private destination. It does not
demonstrate signature bypass or execution of untrusted code. Nevertheless, the
brief explicitly requires ownership-bound cleanup, and the submitted report says
only owned files are removed by checked identity. This deterministic substitution
occurs entirely before reopening; it is distinct from the documented unavoidable
race between a final identity comparison and pathname removal.

Required correction: keep verification/extraction bound to the originally
created snapshot identity, refuse an observed replacement, and check ownership
before removing the snapshot pathname. On refusal, preserve the replacement
while cleaning only still-owned state through retained handles. Add a regression
covering this callback substitution and a replacement observed before successful
snapshot deletion. Any resulting partial-cleanup state must be reported honestly.

## Reviewed strengths and evidence

The command uses independently supplied bounded public trust and the existing
candidate authentication/bundle verifier. Platform, repository, version and path
validation, development opt-in, stable discovery binding, fixed bootstrap source
allowlists, authored errors, archive bounds, fixed-name extraction, member hashes
and modes, and destination/parent substitution checks were inspected. The six-line
source refactor preserves the existing updater allowlist and shared HTTPS,
public-address, redirect and credential controls. The stock tests exercise real
ephemeral signatures and both exact format1 platform layouts without an external
network or executing input binaries.

Independent commands on the isolated exact candidate, before adding the probe:

```sh
go test -race -count=1 ./release/bootstrap ./cmd/minecraft-ui-bootstrap ./internal/selfupdate
go vet ./release/bootstrap ./cmd/minecraft-ui-bootstrap ./internal/selfupdate
GOOS=windows GOARCH=amd64 go test -exec /bin/true ./release/bootstrap ./cmd/minecraft-ui-bootstrap ./internal/selfupdate
```

All passed. The race suite reported bootstrap `5.432s`, command `1.030s` and
selfupdate `14.200s`. `git diff d0994d1 72ea5bb --check` also passed. The added
independent ownership probe fails as described above; the stock suite does not
cover it. No product source, staging, commit, push, local service/account/firewall,
game, or preview mutation was performed during review.

The report correctly limits publication to a receipt written last in a visible
private directory under a caller-controlled parent. No atomic directory rename
is required. Initial trust of the preparer and revalidation at a later privilege
crossing remain integrator obligations. Windows compilation establishes no native
DACL/reparse/lifecycle qualification. Task30 completion, installed behavior,
production trust, and release qualification remain outside this packet.

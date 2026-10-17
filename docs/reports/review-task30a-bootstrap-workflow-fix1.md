# Task30A bootstrap workflow fix1 independent re-review

Specification: **PASS** for the assigned source integration. Quality: **APPROVE**. R1 is closed; zero Critical, Important or Minor findings remain, with zero new findings established.

Reviewed 2026-10-17 against source HEAD `8160174` plus the frozen three-file working diff `.superpowers/sdd/2026-10-17-valheim-parity-completion/task30a-bootstrap-workflow-fix1.diff`. Scope remains only the receipt script, bootstrap checker and candidate workflow. All three working bytes match the companion hash document, rechecked after verification and immediately before this report:

```json
{
  ".github/scripts/candidate-receipts.py": "d99c3564649a0797fb81c0e63940b1711d4c8286926a7030db731e65339bd15c",
  ".github/workflows/candidate-packages.yml": "4190f56ebe206002b09a62e5223924a2990ab1002cff3ac760c9ff856a60cfe2",
  ".github/scripts/candidate-bootstrap-check.py": "2fec0f0c534175c69a35bbd65722a4d8d1afd89567bf51eb8e5c31cf48125719"
}
```

## R1 closure

`output_snapshot` now records the entire prepared directory: its identity/mode/size/times, exact child names, regular-file type, each child's identity/mode/size/times and SHA256, including `completion.json`. File metadata must remain unchanged around hashing, and directory metadata must remain unchanged around enumeration. `lstat` rejects symlinks and unexpected nonregular children. Access times are deliberately absent, so ordinary reads do not invalidate the snapshot.

The checker compares the complete snapshots before setting `existing_output_preserved`. The existing completion hash result is still taken from the bound receipt. Snapshot detail stays in process memory and is not uploaded or logged. This closes the original false preservation claim for corrupted, replaced, added, deleted or mode-changed members.

## Independent executed evidence

Executed locally:

```sh
python /tmp/task30a-workflow-review-fix1.py
```

The exact fixture SHA256 was `fb175c6b20bc7991091cd3b26e866eeb1903b3c1adeb58e24ccbefa86ef54182`. This is the original review's fully reproduced real-preparer/producer fixture with the member-corruption negative control inverted to expect failure. It builds both format fixtures, runs the actual package and outer producers, executes the actual native Linux preparer, exercises the optional native bootstrap receipt path, and models Actions executable-mode loss. Observed:

```text
LOCAL FIXTURE: --bootstrap native version receipt path PASS
LOCAL FIXTURE: actual Linux preparer, both format-validated producer input sets; main() PASS; mode restoration PASS; no bundle member executed
LOCAL FIXTURE: development refusal, authenticated extraction, all 14 member hashes/modes, existing-output refusal PASS
FAIL native bootstrap stage=existing_output_refusal
LOCAL FIXTURE: R1 member corruption correctly fails existing-output preservation
FAIL native bootstrap stage=input_binding
LOCAL FIXTURE: same-size preparer tamper refuses before execution; bounded failure result PASS
```

The corruption control delegates to the real preparer, then changes extracted `LICENSE` after its real existing-output refusal. The result now has `failed: true`, stage `existing_output_refusal`, and no `existing_output_preserved` field.

A separate in-memory negative control replaced only `checker.output_snapshot` with the old receipt-only behavior, then reran the same fixture. The fixture failed at its R1 assertion, proving that the regression detects removal of the full-output barrier. No reviewed source was edited for this control:

```python
source = Path('/tmp/task30a-workflow-review-fix1.py').read_text()
source = source.replace('spec.loader.exec_module(checker)', """spec.loader.exec_module(checker)
def legacy_receipt_only_snapshot(directory):
    return {'completion.json': (None, checker.digest(directory/'completion.json'))}
checker.output_snapshot = legacy_receipt_only_snapshot""", 1)
# Execute with stdout captured. AssertionError is required from the R1 control.
```

Independent focused checks of the actual helper also detected all seven mutations: same-size bytes with restored mtime, member mode, replacement with identical contents, added member, deleted member, symlink substitution, and directory substitution. Reproduction:

```python
import importlib.util, os, tempfile
from pathlib import Path
spec = importlib.util.spec_from_file_location('checker', '.github/scripts/candidate-bootstrap-check.py')
checker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checker)
for label in ('same_size_bytes', 'member_mode', 'member_replacement',
              'added_member', 'deleted_member', 'symlink', 'directory'):
    with tempfile.TemporaryDirectory(prefix='task30a-snapshot-review-') as tmp:
        root = Path(tmp) / 'prepared'
        root.mkdir()
        member = root / 'member'
        member.write_bytes(b'abcd')
        member.chmod(0o600)
        receipt = root / 'completion.json'
        receipt.write_bytes(b'{}\n')
        before = checker.output_snapshot(root)
        if label == 'same_size_bytes':
            value = member.stat()
            member.write_bytes(b'efgh')
            os.utime(member, ns=(value.st_atime_ns, value.st_mtime_ns))
        elif label == 'member_mode':
            member.chmod(0o400)
        elif label == 'member_replacement':
            other = Path(tmp) / 'replacement'
            other.write_bytes(member.read_bytes())
            other.chmod(0o600)
            other.replace(member)
        elif label == 'added_member':
            (root / 'extra').write_bytes(b'x')
        elif label == 'deleted_member':
            member.unlink()
        elif label == 'symlink':
            member.unlink()
            member.symlink_to(receipt)
        elif label == 'directory':
            member.unlink()
            member.mkdir()
        try:
            changed = checker.output_snapshot(root) != before
        except ValueError:
            changed = True
        assert changed, label
```

Both scoped Python scripts also passed `ast.parse(..., feature_version=(3,10))`. Frozen hash checks passed. PowerShell/actionlint executables are unavailable in this local PATH; no independent Windows-shell or actionlint execution is claimed here.

## Workflow additions and remaining gates

The Windows matrix now runs the same actual generated entry-script guard harness separately with `shell: pwsh` and `shell: powershell`, each Windows-only, with the exact generated script/native `.exe` paths and explicit `-Development`. Each step preserves nonzero native exit status and otherwise relies on the harness's terminating exceptions. The separate shell processes avoid retained Add-Type definitions or process-local environment state between versions. There is no added publication, credential, service, account, firewall or game operation.

The outer packet `2739f92` is independently approved in `docs/reports/review-task30a-outer.md`; this review accepts only its workflow composition. The separate A1 Windows parent-path correction remains outside this review. Actual hosted Linux/Windows preparation and PowerShell 7/5.1 behavior still require successful exact-commit runs and evidence inspection. Public-download initial trust, actual acquisition and installed lifecycle remain separate gates; this approval does not complete Task30 or qualify a stable release.

As in the original review, the runtime fixture uses explicitly scoped mocked hosted environment values and an all-zero test SHA. Reports/binaries/artifacts are temporary and removed. Linux manager/updater inputs are tiny command fixtures; Windows inputs and receipts are cross-built/synthetic format fixtures, never Windows execution evidence. Only actual native Linux preparer/version operations and local producer data preparation ran. No bundle member, installer, local service/account/firewall/game or hosted workflow was executed; no product file was edited, staged or committed.

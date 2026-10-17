# Task30A bootstrap workflow integration review

Reviewed 2026-10-17. Specification: PASS for the assigned source integration boundary. Quality: REQUEST CHANGES, one Important finding (R1), zero other findings.

## Frozen scope

Only these files were reviewed for acceptance:

- `.github/scripts/candidate-receipts.py`
- `.github/scripts/candidate-bootstrap-check.py`
- `.github/workflows/candidate-packages.yml`

The exact diff is `.superpowers/sdd/2026-10-17-valheim-parity-completion/task30a-bootstrap-workflow-review.diff`. All three current SHA256 values matched its companion `task30a-bootstrap-workflow-review-hashes.json` before runtime checks and again immediately before writing this report:

```json
{
  ".github/scripts/candidate-receipts.py": "d99c3564649a0797fb81c0e63940b1711d4c8286926a7030db731e65339bd15c",
  ".github/workflows/candidate-packages.yml": "10d97b9b7e1886be0b7bcc5d4d874b51ec9977f5ae7c8bdd3a56f853eb446402",
  ".github/scripts/candidate-bootstrap-check.py": "5a1223005c67bb0c97d4f5a735220205ca347c08c58fba6dfee5e0188ac5e9f9"
}
```

Required project design, plan and ledger context, AGENTS instructions, the read-only wiki hot/index context, accepted A1 acquisition contracts (72ea5bb/f6d2906), and `docs/reports/task30a-outer.md` informed the review. Outer implementation changes, including Windows scratch-child owner hardening, are outside this review and still require their separate acceptance. No package completion or release qualification is asserted.

## R1 — Important: preservation evidence checks only the completion receipt

Location: `.github/scripts/candidate-bootstrap-check.py:115` through the result assignment at line 119.

The initial successful preparation checks all extracted members, but after the second invocation against the existing output the checker checks only the nonzero exit code, empty stdout and unchanged `completion.json` hash. It then records `existing_output_preserved: true`. A refusal that corrupts, deletes or replaces a previously extracted member, adds an unexpected entry, or changes a Linux member mode can therefore produce a successful report.

Reproduction: the full offline fixture below uses the real native Linux preparer and actual candidate/outer producers. A test-only wrapper delegates every invocation to the real preparer; after the real `existing` invocation refuses, it appends bytes to the extracted `LICENSE`. Without changing the checker or completion receipt, the checker exits successfully and reports both `failed: false` and `existing_output_preserved: true`. The fixture asserts that the member actually changed. Observed output:

```text
PASS native development bootstrap preparation; no installed qualification
REPRO R1: existing member corrupted after refusal, checker still reports existing_output_preserved=true and failed=false
```

Requested correction: compare the entire prepared output after refusal, including its exact entry names, ordinary-file/no-symlink status, every member size/hash, the completion receipt size/hash, and Linux modes. Set the preservation result only after those checks. Retain a test-only corruption negative control so the same regression makes the checker fail at `existing_output_refusal`. The accepted A1 source already has no-clobber tests; this finding concerns the stronger claim emitted by this new native workflow evidence.

## Reviewed integration contracts

- Native jobs build the actual separately stamped preparer on each declared OS and execute its version command through the existing receipt path. Helper schema zero is represented by the existing omitted optional field and accepted by `VersionReceipt`/`SnapshotNativeInput`; manager positive schema remains unchanged.
- The package job supplies both exact native preparer paths, the producer's public development trust and a new absolute output path. Its outer asset names and `bootstrap-entry.json` fields match the producer. The five output assets contain no private signing material.
- The prepare matrix selects the correct platform binary and Windows `.exe` suffix. Python resolves the executable to an absolute native path; Linux chmod restores executable mode lost in Actions artifact transport. Local paths passed to the preparer are absolute, and prepared inputs remain data.
- The actual CLI performs authenticated offline extraction. The checker binds the preparer hash/size and public trust digest to same-run unsigned outer provenance, verifies receipt repository/version/schema/OS/architecture/trust/manifest/member fields, checks all expected extracted members and modes, and enforces development opt-in.
- The Windows workflow invokes the actual generated entry script's guard harness through its documented parameters and explicit development switch. Native execution of that harness, its ongoing hardening review, independently trusted public acquisition and lifecycle qualification remain separate gates.
- Trigger is manual; token permissions remain contents-read; checkout credentials are not persisted. There is no release/publish step or production key input. Uploaded bootstrap result fields contain fixed stages, source/version/platform identifiers, booleans, counts and hashes. Private process streams and exception text are not uploaded or printed. Artifact retention is bounded.

## Runtime evidence and limits

Executed `python /tmp/task30a-workflow-review-fixture.py` from the repository using the complete fixture below. It builds tiny Go manager/updater inputs with valid command/build identities for both archive formats and the actual preparer for both targets. Only native Linux version probes and Linux preparer execution occur. Windows inputs are cross-built format fixtures with explicitly synthetic receipts; they supply no Windows execution evidence.

The test uses a scoped `unittest.mock.patch.dict` for the hosted-only environment guard, with an all-zero fixture SHA, without editing any product guard or source. Its JSON reports and all binaries/artifacts are removed by `TemporaryDirectory`. Results are local offline fixture evidence only, never hosted/native Windows/installed/game/release qualification. No services, accounts, firewalls, games, payload installers or bundle members were run. No product files were edited, staged or committed.

Observed successful checks:

```text
Recorded native linux/amd64 minecraft-ui v0.0.1
Recorded native linux/amd64 minecraft-ui-update v0.0.1
Recorded native linux/amd64 minecraft-ui-bootstrap v0.0.1
LOCAL FIXTURE: --bootstrap native version receipt path PASS
LOCAL FIXTURE: actual Linux preparer, both format-validated producer input sets; main() PASS; mode restoration PASS; no bundle member executed
LOCAL FIXTURE: development refusal, authenticated extraction, all 14 member hashes/modes, existing-output refusal PASS
LOCAL FIXTURE: same-size preparer tamper refuses before execution; bounded failure result PASS
```

The last negative control changes preparer bytes without updating outer provenance and asserts that no subprocess helper is called; the failure report remains at `input_binding`. R1 was independently reproduced as described above. Root-supplied actionlint/Python grammar evidence was inspected as task context, not rerun or represented as reviewer-run evidence. Current Windows runner image documentation lists default Python 3.12.10, so this workflow's use of `hashlib.file_digest` has no observed runner-version conflict ([official image inventory](https://github.com/actions/runner-images/blob/main/images/windows/Windows2022-Readme.md)).

## Complete local reproduction

Save this as a temporary file and run with Python 3.11+ from this workspace. It writes only temporary fixture data. The test-only R1 negative control currently expects the demonstrated incorrect success; invert its expected outcome when checking the correction.

```python
import contextlib
import datetime
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
from unittest.mock import patch

repo = Path('/home/thim/Repositories/minecraft-server-ui')
spec = importlib.util.spec_from_file_location('checker', repo / '.github/scripts/candidate-bootstrap-check.py')
checker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checker)

def run(args, **kwargs):
    result = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE, **kwargs)
    if result.returncode:
        print(result.stderr.decode(), file=sys.stderr)
        result.check_returncode()

with tempfile.TemporaryDirectory(prefix='task30a-review-') as temp:
    base = Path(temp)
    fixtures = base / 'fixture-source'
    fixtures.mkdir()
    (fixtures / 'go.mod').write_text('module github.com/jonasthim/minecraft-server-ui\n\ngo 1.27.0\n')
    for command in ('minecraft-ui', 'minecraft-ui-update'):
        path = fixtures / 'cmd' / command
        path.mkdir(parents=True)
        (path / 'main.go').write_text('package main\nimport ("fmt"; "os")\nvar version="dev"\nfunc main(){if len(os.Args)>1 && os.Args[1]=="update-capability" {fmt.Printf(`{"format":1,"version":%q,"schema":16}`,version);return};fmt.Println("'+command+' "+version)}\n')
    inputs = base / 'candidate-input'
    inputs.mkdir()
    for target in ('linux', 'windows'):
        for command in ('minecraft-ui', 'minecraft-ui-update', 'minecraft-ui-bootstrap'):
            binary = inputs / f'{command}-{target}-amd64{ ".exe" if target == "windows" else ""}'
            source = repo if command == 'minecraft-ui-bootstrap' else fixtures
            env = dict(os.environ, GOOS=target, GOARCH='amd64', CGO_ENABLED='0', GOWORK='off')
            run(['go', 'build', '-buildvcs=false', '-trimpath', '-ldflags=-s -w -X main.version=v0.0.1', '-o', str(binary), './cmd/'+command], cwd=source, env=env)
            receipt = dict(format=1, os=target, arch='amd64', command=command, version='v0.0.1', binary_sha256=hashlib.sha256(binary.read_bytes()).hexdigest())
            if command == 'minecraft-ui':
                receipt['schema'] = 16
            Path(str(binary)+'.version.json').write_text(json.dumps(receipt)+'\n')
    # Exercise the optional --bootstrap receipt path with real native version runs.
    receipt_spec = importlib.util.spec_from_file_location('receipts', repo / '.github/scripts/candidate-receipts.py')
    receipts = importlib.util.module_from_spec(receipt_spec)
    receipt_spec.loader.exec_module(receipts)
    receipt_argv = ['receipts','--version','v0.0.1','--target','linux']
    for command, flag in (('minecraft-ui','manager'),('minecraft-ui-update','updater'),('minecraft-ui-bootstrap','bootstrap')):
        path = inputs / (command+'-linux-amd64')
        Path(str(path)+'.version.json').unlink()
        receipt_argv += ['--'+flag,str(path)]
    with patch.dict(os.environ, {'GITHUB_ACTIONS':'true'}), patch.object(sys, 'argv', receipt_argv):
        receipts.main()
    print('LOCAL FIXTURE: --bootstrap native version receipt path PASS')
    artifacts = base / 'candidate-output'
    args = ['go', 'run', './release/cmd/package', '--source', str(repo), '--out', str(artifacts), '--version', 'v0.0.1', '--repository', 'jonasthim/minecraft-server-ui', '--schema', '16', '--published-at', datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ'), '--development']
    for target in ('linux', 'windows'):
        for command, flag in (('minecraft-ui','manager'), ('minecraft-ui-update','updater')):
            args += ['--'+target+'-'+flag, str(inputs / f'{command}-{target}-amd64{ ".exe" if target == "windows" else ""}')]
    run(args, cwd=repo)
    outer = base / 'outer-output'
    run(['go','run','./release/cmd/outer','--repository','jonasthim/minecraft-server-ui','--version','v0.0.1','--linux-preparer',str(inputs / 'minecraft-ui-bootstrap-linux-amd64'),'--windows-preparer',str(inputs / 'minecraft-ui-bootstrap-windows-amd64.exe'),'--trust-file',str(artifacts / 'development-trust.json'),'--out',str(outer),'--development'], cwd=repo)
    binary = outer / 'minecraft-ui-bootstrap-linux-amd64'
    binary.chmod(0o644)  # Model Actions download-artifact mode loss.
    scratch = base / 'runner-temp'
    scratch.mkdir()
    report = base / 'fixture-result.json'
    argv = ['checker','--version','v0.0.1','--target','linux','--binary',str(binary),'--entry-directory',str(outer),'--artifacts',str(artifacts),'--report',str(report)]
    # Test only: permit main() to run on this local host, without changing source.
    # This is an offline fixture test, never native hosted qualification.
    env = {'GITHUB_ACTIONS':'true','RUNNER_ENVIRONMENT':'github-hosted','GITHUB_SHA':'0'*40,'RUNNER_TEMP':str(scratch)}
    with patch.dict(os.environ, env), patch.object(sys, 'argv', argv):
        checker.main()
    result = json.loads(report.read_text())
    assert result['failed'] is False and result['member_count'] == 14
    assert binary.stat().st_mode & 0o777 == 0o755
    print('LOCAL FIXTURE: actual Linux preparer, both format-validated producer input sets; main() PASS; mode restoration PASS; no bundle member executed')
    print('LOCAL FIXTURE: development refusal, authenticated extraction, all 14 member hashes/modes, existing-output refusal PASS')
    # Negative control: model a regression corrupting an existing member on refusal.
    # The current checker incorrectly accepts it because it rechecks only the receipt.
    report.unlink()
    original_run = checker.run_preparer
    corrupted = []
    def corrupt_member_on_refusal(command, scratch, label):
        result = original_run(command, scratch, label)
        if label == 'existing':
            output = Path(command[command.index('--output')+1])
            member = output / 'LICENSE'
            before = member.read_bytes()
            member.write_bytes(before+b'corrupted-on-refusal')
            corrupted.append(member.read_bytes() != before)
        return result
    with patch.dict(os.environ, env), patch.object(sys, 'argv', argv), patch.object(checker, 'run_preparer', corrupt_member_on_refusal):
        checker.main()
    result = json.loads(report.read_text())
    assert corrupted == [True] and result['failed'] is False and result['existing_output_preserved'] is True
    print('REPRO R1: existing member corrupted after refusal, checker still reports existing_output_preserved=true and failed=false')
    # Bind a tampered preparer to the unchanged provenance document: fail before run.
    report.unlink()
    content = bytearray(binary.read_bytes())
    content[-1] ^= 1
    replacement = binary.with_suffix(".tampered")
    replacement.write_bytes(content)
    replacement.replace(binary)
    called = False
    def reject_execution(*args):
        global called
        called = True
        raise AssertionError('tampered input must not execute')
    with patch.dict(os.environ, env), patch.object(sys, 'argv', argv), patch.object(checker, 'run_preparer', reject_execution):
        try:
            checker.main()
        except SystemExit as e:
            assert e.code == 1
    result = json.loads(report.read_text())
    assert result['failed'] is True and result['stage'] == 'input_binding' and called is False
    print('LOCAL FIXTURE: same-size preparer tamper refuses before execution; bounded failure result PASS')
```

# Task32B schema-probe diagnostic review

Integrator review of59dca2e againstceef9f2. Specification PASS; quality APPROVE,
zero findings. This accepts diagnostic source only, with no runtime fix claim.

The fixed runuser account, executable selection, environment, output bound,
schema JSON validation and returned errors remain unchanged. The child marks
only six authored operation boundaries and typed error categories. SQLite data
is limited to the existing concrete driver's bounded numeric code; no error
message, database path, challenge or identity is serialized. The Linux adapter
retains at most8KiB of stderr, rejects an overflow before parsing, accepts only
the exact child grammar, forwards at most one child marker, and otherwise emits
bounded exit/signal or fixed command categories. Success emits no diagnostics.
The journal scrubber rejects unknown, extended, noncanonical or out-of-range
values and preserves previous summary fields.

Independent execution:

```text
go test -race -count=1 ./internal/selfupdate ./cmd/minecraft-ui \
  -run 'Test(LinuxSchemaProbe|SchemaProbe|CandidateProbesKeepFixedManagerAccountBoundary)'
selfupdate PASS4.098s; command PASS2.917s
python3 -B -m unittest discover -s deploy/qualification -p summarize_journal_test.py -v
4tests PASS0.067s
```

The subprocess tests actually exercise failed probe boundaries, process
exit/signal/launch/output outcomes and successful output. Worker negative-overlay
evidence is recorded separately in task32b-linux-abort.md. Root inspected that
test implementation and the seven-file frozen diff. No services or games ran
locally. Actual Ubuntu24 failure remains known only at `schema_probe` until a
new exact-source hosted run supplies these diagnostics.

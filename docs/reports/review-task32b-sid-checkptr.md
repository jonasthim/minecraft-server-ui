# Windows updater SID validation correction review

Integrator specification/quality review: PASS, no findings.

Root inspected the failing native CI39041677923/job117422273922 stack and pinned
x/sys v0.48.0 SID.Copy, SubAuthorityCount and generated syscall implementation.
The failing access obtains a native interior pointer into Go-owned SID memory.
The correction removes those accesses and validates the same fixed account SID
shape with canonical bounded uint32 decimal components before native parsing.
RID1000 is inclusive; reserved lower RIDs, broad/builtin/virtual identities,
alternate encodings, wrong component counts and overflow remain rejected.

Native SID validity and exact String round-trip checks remain. Process DACL
construction, exact query-only grant, preservation/readback checks, pinning,
pipe/PID/image binding and the race detector are unchanged. Root reran Windows
test compilation and vet successfully and inspected the added boundary cases.

Actual Windows race execution is required to close the observed runtime failure.
The prior installed non-race process-access qualification proves the original
grant works on that host, not that this corrected source has passed native CI.

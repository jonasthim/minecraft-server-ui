# Linux schema-probe execution context review

Integrator specification/quality review after fix round1: PASS, zero remaining
findings. Actual UID transition failure remains unresolved pending hosted data.

The probe snapshots bounded process and calling-thread proc fields, pins the
calling goroutine through child Start, then releases the thread before Wait.
It changes no credentials or service restrictions. Exact typed parsers emit
only root/nonroot/unknown, CAP_SETUID membership, NNP/seccomp and UID-map class;
raw values and other proc fields never enter output. Context is emitted only on
the existing failure/invalid-response paths and preserves their original error.

Root required effective loaded updater/recovery unit properties instead of an
uninstantiated template. The revised harness scrubs a16KiB property input into
fixed metadata JSON. Root also required all UID fields and UID-map bounds to be
validated, with malformed values represented as unknown, and the finite marker
validator enforced at emission. These corrections are present.

Root reran focused schema/context race tests: PASS4.125s; all9 Python journal/unit
scrubber tests PASS; shell syntax PASS. Worker complete selfupdate race/vet also
passed. The harmless id child checks inherited root classification without
changing any local account/service/namespace. Actual updater unit policy and
kernel behavior require another hosted run. This packet cannot establish which
policy caused setuid EPERM or justify removing restrictions.

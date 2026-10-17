# Task30A native bootstrap evidence and harness exit correction

Run39039050240 at exacte6846d87847affc8aa6ebd4ca8b7135e8f0333ed built both
preparers natively, generated candidate bundles/entry scripts and passed the
shipping verifier. Actual offline preparation passed on Ubuntu24.04 and Windows
Server2022, including development default refusal, exact member hashes and
existing-output preservation. Linux artifact12103770467 has14members and preparer
SHA25644cd99947b6726a864ee24dbb4014f680c07b6d4dedda5b88e41386946b79a7b;
Windows artifact12103427848 has5members and preparer SHA256
52463a22bf1f79c707160dd6fba3962d2317ab64c30d22fcdcfcf3158c96aa76.

Windows job117415258128 also passed the PowerShell7 outer behavioral assertions,
including actual Administrators-owned protected ACLs on all four scratch files,
hash refusal, retained-image sharing and cleanup tests. The harness printed its
success line, then the workflow failed with exit1: the intentional final refusal
case left LASTEXITCODE=1. The WindowsPowerShell5.1 step was consequently skipped.
The overall run remains failure. This is a test-result propagation defect, not a
passed5.1 gate or a demonstrated product installer failure.

The standalone harness now explicitly exits0 only after every assertion and its
finally cleanup return successfully. Exceptions exit before this final statement.
Integrator review accepts this three-line source correction, zero findings. A
pure official portable PowerShell7.6.7 language fixture executes an intentional
child exit1 followed by the actual final statement and obtains0; injecting a
terminating assertion before it retains exit1. The full native gate must rerun.
No payload installer/service/account/game or local Windows function was executed
for this language fixture.

Separately CI39039040686 at the same source passes both native hosts. Root
inspected the Windows log: availability diagnostics/reply grammar/actual process
image and invalid pipe handle tests pass, all prior pipe tests pass, and both
parent-rename tests observe denial while handles are retained plus successful
rename after release (normal completion and cancellation). This accepts the
parent test correction's native execution. CI39039278464 ata37cf15 also passes
both native hosts, including the later observation-admission source.

These results qualify development preparation and the stated native/source test
scope only. Public trust distribution, public pinned/latest acquisition, actual
Windows entry-script process marshaling, installer lifecycle and release support
remain separate gates.

# Task30A Windows parent guard — integrator review

Specification PASS; quality APPROVE; zero findings. Reviewed only
release/bootstrap/prepare_test.go and task30a-windows-parent-guard.md against
3d462eb. No production handle, ACL, path or cryptographic guard changes.

The existing native failure proves the POSIX-only test assumption. The revised
Windows branch accepts only permission denial, checks parent/output/caller-file
identity and absent moved destination, and requires the identical rename to
succeed after Prepare returns. This last assertion prevents an unrelated
persistent permission failure or leaked handle from passing. Successful prepare
checks the receipt, every member hash and removed snapshot; cancellation checks
complete owned-output cleanup and caller-data preservation. Platforms allowing
rename still execute real substitution, require refusal and preserve foreign
replacement/caller data. Neither path is skipped.

Independent scoped race execution of TestOutputNoClobberAndPathBinding on Linux
passes, as does the scoped diff check. This executes the actual substitution
branches; revised Windows retained-handle denial branches still need native
hosted execution. Worker Windows compilation is accurately labelled. Accepted
for the next exact-source CI/bootstrap workflow, not native qualification.

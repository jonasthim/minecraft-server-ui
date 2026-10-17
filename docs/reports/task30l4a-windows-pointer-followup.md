# Native Windows snapshot pointer follow-up

CI39046943746 at06221bb passed Linux and failed Windows. Windows job117437439432
reached the new binary ACL parser and aborted under checkptr in
`windows.getSidSubAuthorityCount`, through `privateAllowACE`, in both CLI and
managerstate snapshot fixtures. This is direct native evidence; the earlier
326876f assertion-only failures predated this parser and could not validate it.

Root replaced only the count accessor with a direct byte read using unsafe.Add
from the retained SID pointer, after validating the minimum ACE/SID header.
The same approach is already used by the native updater's ACL validator, which
has passed Windows race execution. The maximum subauthority count, ACE extent,
native SID validity, exact trustees/flags/rights and identity checks remain.
Existing native snapshot and explicit ACL fixtures exercise this path; race
checking stays enabled. Another actual Windows run is required.

The same run timed out after3s in the setup of
TestCoordinatorBootIntentCrashReconstructsWorldRiskWithoutOldPointers, while
its job was running at publishing_backup and the update journal was committed.
It had not reached the injected crash or recovery assertions. This fixture's
setup now uses the existing bounded30s observation helper, matching the other
composed backup/activation tests. The ordinary helper and all product deadlines
stay unchanged. All crash reconstruction and world-risk assertions still run;
this adjustment does not establish the delayed job would have succeeded.

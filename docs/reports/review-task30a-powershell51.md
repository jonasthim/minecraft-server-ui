# PowerShell5.1 outer fixture correction review

Integrator review after fix round1: PASS, zero remaining findings.

Plain JSON assignment preserves the parsed argument array on both documented
PowerShell enumeration behaviors. Explicit array/count/string/exact-value checks
now validate every argument, including repository/trust path and development
flag. Root compared the expected sequence with the unchanged shipping template;
URL extraction retains the original exact pinned URL using separate captures.
No downloaded code is executed and native ACL/hash/cleanup assertions remain.

Root caught an initially unhandled IndexOutOfRange in the new baseline negative
control under StrictMode Latest. The worker's first extracted-fixture run had
omitted strict mode. The corrected control catches the expected exception;
strict mode remains enabled. Root independently reran the exact complete fixture
prefix with verified portable PowerShell7.6.7: PASS, including native/legacy
enumeration and malformed/truncated/changed argument refusal. The worker report
corrects the earlier incomplete verification claim.

The original hosted5.1 log has no inner assertion line; matching that failure
to JSON enumeration remains a source-backed inference until the actual corrected
Windows5.1 gate runs. This review accepts the fixture source for that rerun.

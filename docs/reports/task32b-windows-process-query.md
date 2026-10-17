# Task32B Windows updater process query grant

Implemented against frozen integrator scope after actual run39038823522 at
8160174 reported process_open/Win32code5 from the manager while both services
ran. Windows artifact12102568912 records that exact failed identity-check step.
The existing PROCESS_QUERY_LIMITED_INFORMATION image verification remains.

The LocalSystem updater reads its protected account receipt before announcing
Running or recovering/startup work. It accepts a canonical conventional local or
domain account SID of the formS-1-5-21-A-B-C-RID with RID>=1000; built-in, broad,
virtual-service and reserved low-RID identities refuse this grant. This matches
the current dedicated credential-bearing account deployment and does not claim
new virtual-account support.

A handle-bound process DACL read must have trusted SYSTEM/Administrators owner,
a valid group, nonnull canonical basic allow/deny entries, retained SYSTEM and
Administrators permissions and no unexpected allow principal. Existing denies
are preserved; query-conflicting denies, broad grants or ambiguous manager
entries refuse. ACLFromEntries adds exactly0x1000 for the protected manager SID.
The merge preserves original ACE bytes/order/rights and is idempotent. Only the
current updater process DACL is published; no owner/group/SACL/integrity label,
account right, registry ACL, file ACL, SCM permission or token privilege changes.
Readback requires the exact merged ACL and original owner/group/control. Failure
keeps startup closed with appended fixed SCM stage7, optional numeric Win32 code.
No raw SID/error/path is logged. Pipe authentication and both-direction exact
SCM/image/file/PID checks remain unchanged.

Microsoft's [process security documentation](https://learn.microsoft.com/en-us/windows/win32/procthread/process-security-and-access-rights)
identifies0x1000 as the limited query access used for image names, and documents
that OpenProcess checks the process DACL. The implementation uses handle-bound
[SetSecurityInfo](https://learn.microsoft.com/en-us/windows/win32/api/aclapi/nf-aclapi-setsecurityinfo)
for the DACL alone and [SetEntriesInAcl](https://learn.microsoft.com/en-us/windows/win32/api/aclapi/nf-aclapi-setentriesinaclw)
for the exact additional grant. The next native run must establish behavior of
the real service descriptor; source inspection cannot prove native access.

## Qualification composition

The disposable qualifier gains only a no-argument process-access command. The
Windows harness runs it with its existing in-memory manager PSCredential after
native service/PID/image/SID assertions. It queries the fixed updater service,
opens only limited-query access and verifies the fixed image. It then separately
attempts stronger handle rights and requires exact ACCESS_DENIED for terminate,
VM read/write/operation, duplicate-handle, create-thread, write-DACL and write-owner.
It never invokes any corresponding operation. A second SCM query verifies the
same service PID/state. Only format1 and ten fixed booleans are emitted.

The root-owned harness gives the child30seconds, bounds private output4KiB,
requires exact keys/integer format/actual true booleans and writes a reconstructed
summary. Unknown fields or string/numeric truthy values refuse. Private stderr,
credentials, raw process security and command output are not uploaded. The Linux
command stub refuses this Windows-only evidence operation.

## Verification and limits

The worker quota ended after authoring the implementation, qualifier and native
unit cases. Root then compiled both Windows test binaries, ran Windows vet on
selfupdate/qualifier/manager, ran the Linux qualifier race tests1.031s, reviewed
all changed source and ran the existing12 updater API regressions0.031s.
The pure support parser was executed using the verified official portable Linux
PowerShell7.6.7: valid evidence plus eight rejection controls pass (false, string,
number, missing/extra field, wrong format, array, oversize); both modified scripts
parse. This is language evidence, not Windows execution.

Windows-specific tests cover exact merged masks/principals/preserved denies,
idempotence, unsafe descriptors/SIDs, cancellation/invalid handle and missing
receipt refusal before Running. They compile but have not yet executed. The
installed process-access probe and genuine activation/rollback/recovery must
pass on GitHub before native correction is accepted. No local native services,
accounts, firewall, games or user preview were changed.

import { useCallback, useEffect, useRef, useState } from 'react';
import { Alert, Anchor, Badge, Button, Checkbox, Divider, Group, Loader, Modal, NumberInput, Paper, Progress, Select, SimpleGrid, Stack, Text, TextInput, Title } from '@mantine/core';
import { Link, useParams } from 'react-router-dom';
import { api } from '../../api/client';
import type { components, operations } from '../../api/schema';
import { useAuth } from '../auth/AuthProvider';
import { emptyPack, restorePack, changePackSource } from './draft';
import { PackReview } from './PackReview';
import { ContentManualReview } from './ContentManualReview';

type Plan = components['schemas']['UpdatePlan'];
type Status = components['schemas']['UpdateStatus'];
type Request = components['schemas']['UpdateRequest'];
type Job = components['schemas']['Job'];
type Rollback = components['schemas']['UpdateRollbackPreview'];
type Detail = components['schemas']['InstanceDetail'];
const message = (error: unknown) => error instanceof Error ? error.message : 'Request failed. Try again.';
const deadlinePassed = (deadline: string) => !Number.isFinite(Date.parse(deadline)) || Date.parse(deadline) <= Date.now();
const active = (job: Job | null) => !!job && (job.state === 'queued' || job.state === 'running');
const selection = (value: components['schemas']['VersionSelection']) => [value.minecraft_version, value.flavor, value.loader_version].filter(Boolean).join(' / ');
const historyLabel = (state: string) => ({ rolled_back: 'Previous executable restored', boot_attempted: 'New version attempted start', blocked: 'Recovery required', superseded: 'Continued by confirmed rollback', complete: 'Completed', prepared: 'Safety backup ready', publishing: 'Applying files', committed: 'Selection committed' })[state] ?? state;

export function UpdatesPage() {
  const { instanceId = '' } = useParams();
  const { user } = useAuth();
  const operator = user?.role === 'operator' || user?.role === 'admin';
  const admin = user?.role === 'admin';
  const root = '/instances/' + encodeURIComponent(instanceId);
  const endpoint = root + '/updates';
  const [detail, setDetail] = useState<Detail | null>(null);
  const [status, setStatus] = useState<Status | null>(null);
  const [previews, setPreviews] = useState<Plan[]>([]);
  const [content, setContent] = useState<components['schemas']['ContentManifest'] | null>(null);
  const [replacements, setReplacements] = useState<NonNullable<Request['content_selections']>>({});
  const [contentReceipts, setContentReceipts] = useState<NonNullable<Request['content_manual']>>({});
  const [contentFiles, setContentFiles] = useState<Record<string, File | null>>({});
  const [replacePack, setReplacePack] = useState(false);
  const [packRequest, setPackRequest] = useState<NonNullable<Request['pack']>>(emptyPack);
  const [dirty, setDirty] = useState(false);
  const [contentError, setContentError] = useState('');
  const [archiveFile, setArchiveFile] = useState<File | null>(null);
  const [memberFiles, setMemberFiles] = useState<Record<string, File | null>>({});
  const [verifiedFiles, setVerifiedFiles] = useState<Record<string, { sha256: string; size: number }>>({});
  const pending = useRef<{ request: Request; existing: Set<string> } | null>(null);
  const drafts = useRef(new Map<string, Request>());
  const [preview, setPreview] = useState<Plan | null>(null);
  const [job, setJob] = useState<Job | null>(null);
  const [mode, setMode] = useState<'manual' | 'automatic'>('automatic');
  const [minecraft, setMinecraft] = useState('');
  const [flavor, setFlavor] = useState<components['schemas']['VersionSelection']['flavor']>('vanilla');
  const [loader, setLoader] = useState('');
  const [countdown, setCountdown] = useState(300);
  const [confirmed, setConfirmed] = useState(false);
  const [applyDialog, setApplyDialog] = useState(false);
  const [rollback, setRollback] = useState<Rollback | null>(null);
  const [rollbackConfirmation, setRollbackConfirmation] = useState('');
  const [rewindAcknowledged, setRewindAcknowledged] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [finalDetailsError, setFinalDetailsError] = useState('');
  const [finalDetailsLoading, setFinalDetailsLoading] = useState(false);
  const [finalAttempt, setFinalAttempt] = useState(0);
  const [deadlineTick, tickDeadline] = useState(0);
  useEffect(() => {
    const remaining = [preview?.expires_at, rollback?.expires_at].filter((deadline): deadline is string => !!deadline).map(deadline => Date.parse(deadline) - Date.now()).filter(delay => Number.isFinite(delay) && delay > 0);
    if (!remaining.length) return;
    const timer = window.setTimeout(() => tickDeadline(value => value + 1), Math.min(...remaining, 2147483646) + 1);
    return () => window.clearTimeout(timer);
  }, [preview?.expires_at, rollback?.expires_at, deadlineTick]);
  const refresh = useCallback(async (signal?: AbortSignal) => {
    const [info, current, plans, manifest] = await Promise.all([
      api<operations['instancesGet']['responses'][200]['content']['application/json']>(root, { signal }),
      api<Status>(endpoint, { signal }),
      operator ? api<Plan[]>(endpoint + '/previews', { signal }) : Promise.resolve([] as Plan[]),
      api<components['schemas']['ContentManifest']>(root + '/content', { signal }).catch(error => { if (!signal?.aborted) setContentError(message(error)); return null; }),
    ]);
    if (signal?.aborted) return [];
    setDetail(info.detail); setStatus(current); setPreviews(plans); setContent(manifest); if (manifest) setContentError('');
    return plans;
  }, [root, endpoint, operator]);
  useEffect(() => {
    const controller = new AbortController();
    setDetail(null); setStatus(null); setPreview(null); setJob(null); setRollback(null); setError(''); setDirty(false); setReplacements({}); setContentReceipts({}); setContentFiles({}); setPackRequest(emptyPack()); setReplacePack(false); setMemberFiles({}); setArchiveFile(null); setVerifiedFiles({}); pending.current = null; drafts.current.clear();
    void refresh(controller.signal).then(() => undefined).catch(error => { if (!controller.signal.aborted) setError(message(error)); });
    return () => controller.abort();
  }, [refresh]);
  useEffect(() => {
    if (!detail) return;
    setMinecraft(detail.instance.minecraft_version); setFlavor(detail.instance.flavor); setLoader(detail.instance.loader_version ?? '');
  }, [detail?.instance.id]);
  const activeJob = active(job);
  useEffect(() => {
    if (!job) return;
    const controller = new AbortController();
    let polling = false;
    let timer: number | undefined;
    const publishTerminal = (terminal: Job) => {
      // These facts are authoritative even when secondary endpoints are down.
      setJob(terminal);
      if (terminal.state === 'succeeded') {
        setError('');
        setNotice(terminal.kind === 'instance_update_prepare' ? 'Preparation completed. Loading final preview details.' : terminal.kind === 'instance_rollback' ? 'Confirmed rollback completed. The server remains stopped.' : 'Update completed. The full safety backup is retained.');
      } else {
        setNotice('');
        setError(terminal.error || (terminal.state === 'cancelled' ? 'The operation was cancelled.' : 'The operation did not complete. Inspect job output before retrying.'));
      }
    };
    const readFinalDetails = async (terminal: Job) => {
      setFinalDetailsLoading(true);
      const [metadata, output] = await Promise.allSettled([
        refresh(controller.signal),
        terminal.state === 'succeeded' ? Promise.resolve(null) : api<{ logs: components['schemas']['JobLog'][] }>('/jobs/' + encodeURIComponent(terminal.id) + '/logs', { signal: controller.signal }),
      ]);
      if (controller.signal.aborted) return;
      const failures: string[] = [];
      if (metadata.status === 'rejected') failures.push('Update details could not be refreshed: ' + message(metadata.reason));
      else if (terminal.state === 'succeeded' && terminal.kind === 'instance_update_prepare') {
        const plans = metadata.value;
        if (pending.current) {
          const candidates = plans.filter(plan => !pending.current?.existing.has(plan.id));
          const next = candidates.length === 1 ? candidates[0] : null;
          if (next) {
            drafts.current.set(next.id, pending.current.request);
            if (next.pack && pending.current.request.pack) setPackRequest({ ...pending.current.request.pack, selections: { ...next.pack.selections, ...pending.current.request.pack.selections } });
          }
          setPreview(next); setDirty(false); setConfirmed(false); pending.current = null;
          setNotice(next ? 'Preview ready. Inspect the exact changes and blockers below.' : 'Preparation completed. Choose the prepared preview to inspect its exact changes.');
        }
      } else if (terminal.state === 'succeeded') setPreview(null);
      if (output.status === 'rejected') failures.push('Saved job output could not be loaded: ' + message(output.reason));
      else if (output.value) {
        const lastLine = output.value.logs.at(-1)?.message;
        if (lastLine) setError(terminal.error && lastLine !== terminal.error ? terminal.error + ' ' + lastLine : lastLine);
      }
      setFinalDetailsError(failures.join(' ')); setFinalDetailsLoading(false);
    };
    const poll = async () => {
      if (polling) return; polling = true;
      try {
        const response = await api<{ job: Job }>('/jobs/' + encodeURIComponent(job.id), { signal: controller.signal });
        if (controller.signal.aborted) return;
        if (active(response.job)) { setJob(response.job); setError(''); }
        else {
          window.clearInterval(timer);
          publishTerminal(response.job);
          await readFinalDetails(response.job);
        }
      } catch (error) { if (!controller.signal.aborted) setError(message(error)); }
      finally { polling = false; }
    };
    if (active(job)) {
      setFinalDetailsError(''); setFinalDetailsLoading(false);
      timer = window.setInterval(() => { void poll(); }, 1000);
    } else {
      publishTerminal(job);
      void readFinalDetails(job);
    }
    // Own reads by job ID, not active state: publishing a terminal result must
    // not abort its own best-effort final reads. A new job/unmount still aborts.
    return () => { controller.abort(); window.clearInterval(timer); };
  }, [job?.id, finalAttempt, refresh]);
  function invalidate() { setDirty(true); setConfirmed(false); setApplyDialog(false); }
  function choosePreview(plan: Plan | null) {
    setPreview(plan); setDirty(false); setConfirmed(false); setMemberFiles({}); setArchiveFile(null); setVerifiedFiles({});
    if (!plan) return;
    const saved = drafts.current.get(plan.id);
    setMode(plan.mode); setMinecraft(plan.target.minecraft_version); setFlavor(plan.target.flavor); setLoader(plan.target.loader_version ?? ''); setCountdown(plan.countdown_seconds);
    setReplacements(saved?.content_selections ?? {}); setContentReceipts(saved?.content_manual ?? {}); setContentFiles({}); setReplacePack(!!plan.pack);
    setPackRequest(saved?.pack ?? (plan.pack ? restorePack(plan.pack) : emptyPack()));
    if (!saved) setNotice('Stored preview loaded exactly as prepared. Editing creates a new preview; private upload receipts and standalone replacements may need selecting again.');
  }
  async function prepare(nextPack = packRequest, nextReplacements = replacements, nextReceipts = contentReceipts) {
    setBusy(true); setError(''); setNotice('');
    const request: Request = { mode, countdown_seconds: countdown, ...(mode === 'manual' ? { selection: { flavor, minecraft_version: minecraft.trim(), ...(flavor === 'vanilla' ? {} : { loader_version: loader.trim() }) }, content_selections: nextReplacements, content_manual: nextReceipts, ...(replacePack ? { pack: nextPack } : {}) } : {}) };
    try {
      const result = await api<components['schemas']['UpdateJobResponse']>(endpoint + '/previews', { method: 'POST', body: JSON.stringify(request) });
      pending.current = { request, existing: new Set(previews.map(plan => plan.id)) };
      setJob(result.job); setPreview(null); setNotice('Preparing verified files and compatibility checks. Your server keeps its current selection and process state.');
    } catch (error) { setError(message(error)); } finally { setBusy(false); }
  }
  async function uploadArchive() {
    const expected = preview?.pack?.archive_manual;
    if (!admin || !expected || !archiveFile || dirty || mode !== 'manual') return;
    setBusy(true); setError(''); setNotice('Verifying the official archive. No update is being applied.');
    try {
      const receipt = await api<components['schemas']['ModpackArchiveReceipt']>(root + '/modpack/archives?' + new URLSearchParams(expected.selection), { method: 'POST', headers: { 'Content-Type': 'application/octet-stream' }, body: archiveFile });
      const next = { ...packRequest, archive_receipt_id: receipt.id };
      setPackRequest(next); setVerifiedFiles(current => ({ ...current, archive: receipt })); setArchiveFile(null); invalidate();
      await prepare(next);
    } catch (error) { setError(message(error)); setNotice(''); } finally { setBusy(false); }
  }
  async function uploadMember(path: string) {
    const file = memberFiles[path];
    if (!admin || !preview || !file || dirty || mode !== 'manual') return;
    setBusy(true); setError(''); setNotice('Verifying the exact publisher file. No update is being applied.');
    try {
      const receipt = await api<components['schemas']['UpdatePackMemberReceipt']>(endpoint + '/previews/' + encodeURIComponent(preview.id) + '/pack/members?' + new URLSearchParams({ path }), { method: 'POST', headers: { 'Content-Type': 'application/octet-stream' }, body: file });
      if (receipt.path !== path) throw new Error('The verified file does not match the requested pack path. Reload the preview before retrying.');
      const next = { ...packRequest, manual: { ...packRequest.manual, [path]: receipt.receipt_id } };
      setPackRequest(next); setVerifiedFiles(current => ({ ...current, [path]: receipt })); setMemberFiles(current => ({ ...current, [path]: null })); invalidate();
      await prepare(next);
    } catch (error) { setError(message(error)); setNotice(''); } finally { setBusy(false); }
  }
  async function uploadContent(id: string) {
    const expected = preview?.content_manual_required?.[id];
    const file = contentFiles[id];
    if (!admin || !expected || !file || dirty || mode !== 'manual') return;
    setBusy(true); setError(''); setNotice('Staging the content publisher file for a new verification preview. No update is being applied.');
    try {
      const upload = await api<components['schemas']['ContentUpload']>(root + '/content/uploads?' + new URLSearchParams({ filename: file.name }), { method: 'POST', headers: { 'Content-Type': 'application/octet-stream' }, body: file });
      const nextReplacements = { ...replacements, [id]: expected.selection };
      const nextReceipts = { ...contentReceipts, [id]: upload.id };
      setReplacements(nextReplacements); setContentReceipts(nextReceipts); setContentFiles(current => ({ ...current, [id]: null })); setVerifiedFiles(current => ({ ...current, ['content:' + id]: upload })); invalidate();
      await prepare(packRequest, nextReplacements, nextReceipts);
    } catch (error) { setError(message(error)); setNotice(''); } finally { setBusy(false); }
  }
  function clearContentReceipt(id: string) {
    setContentReceipts(current => { const next = { ...current }; delete next[id]; return next; });
    setVerifiedFiles(current => { const next = { ...current }; delete next['content:' + id]; return next; });
  }
  async function apply() {
    if (preview && deadlinePassed(preview.expires_at)) { tickDeadline(value => value + 1); setConfirmed(false); setError('This preview expired. Prepare another preview before applying.'); return; }
    if (!canApply || !preview || !confirmed) return; setBusy(true); setError(''); setNotice('');
    try { const result = await api<components['schemas']['UpdateJobResponse']>(endpoint + '/previews/' + encodeURIComponent(preview.id) + '/apply', { method: 'POST', body: JSON.stringify({ confirmed: true } satisfies components['schemas']['UpdateApplyRequest']) }); setJob(result.job); setApplyDialog(false); setNotice('Update queued. A successful full safety backup is required before any stop or activation.'); }
    catch (error) { setError(message(error)); } finally { setBusy(false); }
  }
  async function previewRollback(transaction: string) {
    setBusy(true); setError(''); setRollbackConfirmation(''); setRewindAcknowledged(false);
    try { setRollback(await api<Rollback>(endpoint + '/rollbacks/previews', { method: 'POST', body: JSON.stringify({ transaction_id: transaction } satisfies components['schemas']['UpdateRollbackPreviewRequest']) })); }
    catch (error) { setError(message(error)); } finally { setBusy(false); }
  }
  async function applyRollback() {
    if (!rollback || !admin || !rewindAcknowledged || rollbackConfirmation !== instanceId) return;
    if (deadlinePassed(rollback.expires_at)) { tickDeadline(value => value + 1); setError('This rollback preview expired. Close the dialog and review rollback again.'); return; }
    setBusy(true); setError(''); setNotice('');
    try { const result = await api<components['schemas']['UpdateJobResponse']>(endpoint + '/rollbacks/' + encodeURIComponent(rollback.id) + '/apply', { method: 'POST', body: JSON.stringify({ confirmation: rollbackConfirmation, acknowledge_world_rewind: true } satisfies components['schemas']['UpdateRollbackApplyRequest']) }); setJob(result.job); setRollback(null); setNotice('Confirmed instance rollback queued. Current data must first pass a new full safety backup.'); }
    catch (error) { setError(message(error)); } finally { setBusy(false); }
  }
  const working = busy || activeJob;
  const blocked = !!status?.blockers.length;
  const expired = !!preview && deadlinePassed(preview.expires_at);
  const rollbackExpired = !!rollback && deadlinePassed(rollback.expires_at);
  const packIncomplete = mode === 'manual' && replacePack && (!packRequest.pack.project_id.trim() || !packRequest.pack.version_id.trim() || !packRequest.pack.file_id.trim());
  const contentIncomplete = Object.values(replacements).some(item => !item.project_id.trim() || !item.version_id.trim() || !item.file_id.trim());
  const packBlocked = !!preview?.pack && (!!preview.pack.archive_manual || preview.pack.manual.length > 0 || preview.pack.blockers.length > 0 || preview.pack.changes.some(change => change.action === 'conflict'));
  const canApply = operator && !!preview && !working && !blocked && !expired && !dirty && !preview.blockers.length && !packBlocked && !Object.keys(preview.content_manual_required ?? {}).length && preview.changes.length > 0;
  return <Stack gap="xl">
    <div><Anchor component={Link} to={root}>Back to server</Anchor><Group justify="space-between" align="start" mt="sm"><div><Title order={2}>Updates{detail ? ' · ' + detail.instance.name : ''}</Title><Text c="dimmed" mt={4}>Review the next version before changing your server.</Text></div><Button variant="light" disabled={busy} onClick={() => void refresh().catch(error => setError(message(error)))}>Refresh updates</Button></Group></div>
    {error && <Alert color="red" role="alert">{error}</Alert>}{notice && <Alert color="blue" role="status">{notice}</Alert>}
    {!status && !error && <Loader aria-label="Loading update history" />}
    {status?.blockers.map((blocker, index) => <Alert key={index} color="yellow" title="Recovery or conflict needs attention">{blocker}</Alert>)}
    {detail && <Paper withBorder p="lg"><Stack><Title order={3} size="h4">Choose an update</Title><Text>Current selection: {selection(detail.instance)}</Text><Text size="sm" c="dimmed">Pinned updates keep this exact Minecraft version and server software. Manual changes require compatibility checks, a full safety backup, and your confirmation.</Text>
      {operator ? <><Select label="Update mode" data={[{ value: 'automatic', label: 'Check updates for the pinned Minecraft version' }, { value: 'manual', label: 'Choose an exact Minecraft version' }]} value={mode} disabled={working} onChange={value => { setMode(value === 'manual' ? 'manual' : 'automatic'); invalidate(); }} />
        {mode === 'manual' && <SimpleGrid cols={{ base: 1, sm: 3 }}><TextInput label="Minecraft version" placeholder="1.21.11" required value={minecraft} disabled={working} onChange={event => { setMinecraft(event.currentTarget.value); invalidate(); }} /><Select label="Server software" data={['vanilla', 'paper', 'fabric', 'forge', 'neoforge']} value={flavor} disabled={working} onChange={value => { if (value) setFlavor(value as typeof flavor); setLoader(''); invalidate(); }} /><TextInput label={flavor === 'paper' ? 'Exact Paper build' : 'Exact loader version'} description={flavor === 'vanilla' ? 'Vanilla has no loader.' : 'Use an exact official build or loader.'} disabled={working || flavor === 'vanilla'} value={loader} onChange={event => { setLoader(event.currentTarget.value); invalidate(); }} /></SimpleGrid>}
        <NumberInput label="Warning countdown (seconds)" description="Five minutes by default. A running server must acknowledge player observation and announcements before stopping." min={0} max={3600} allowDecimal={false} value={countdown} disabled={working} onChange={value => { setCountdown(Number(value)); invalidate(); }} />
        <Text size="sm" c="dimmed">Unknown uploads, missing dependencies, required manual files, incompatible pinned items, and pack conflicts block activation.</Text>
        {mode === 'manual' && <>
          <Divider label="Compatible content" />
          <Text size="sm">Retain installed artifacts or choose exact publisher replacements for the target version. <Anchor component={Link} to="/catalog">Browse the content catalog</Anchor>.</Text>
          {contentError && <Alert color="yellow">Installed content could not be loaded: {contentError}. Refresh updates before choosing standalone replacements.</Alert>}
          {content?.items.map(item => <Stack key={item.id} gap="xs" style={{ overflowWrap: 'anywhere' }}>
            <Text fw={600}>{item.name} {item.version}{item.pinned ? ' · pinned' : ''}{item.unknown ? ' · unknown upload' : ''}{item.pack_member ? ' · pack member' : ''}</Text>
            {item.selection && !item.pack_member ? <>
              <Text size="sm" c="dimmed">Publisher: {item.selection.provider} · Project: {item.selection.project_id}</Text><Checkbox label={'Replace ' + item.name + ' with an exact publisher file'} disabled={working || item.pinned} checked={!!replacements[item.id]} onChange={event => { const checked = event.currentTarget.checked; setReplacements(current => { const next = { ...current }; if (checked && item.selection) next[item.id] = { ...item.selection }; else delete next[item.id]; return next; }); clearContentReceipt(item.id); invalidate(); }} />
              {replacements[item.id] && <SimpleGrid cols={{ base: 1, sm: 2 }}>
                <TextInput label={'Replacement publisher version for ' + item.name} required maxLength={256} value={replacements[item.id].version_id} disabled={working || item.pinned} onChange={event => { const value = event.currentTarget.value; setReplacements(current => ({ ...current, [item.id]: { ...current[item.id], version_id: value } })); clearContentReceipt(item.id); invalidate(); }} />
                <TextInput label={'Exact publisher file ID for ' + item.name} required maxLength={256} description="Select the exact file when a release contains several files." value={replacements[item.id].file_id} disabled={working || item.pinned} onChange={event => { const value = event.currentTarget.value; setReplacements(current => ({ ...current, [item.id]: { ...current[item.id], file_id: value } })); clearContentReceipt(item.id); invalidate(); }} />
              </SimpleGrid>}
            </> : <Text size="sm" c="dimmed">{item.pack_member ? 'Select a compatible pack to replace this member.' : 'Unknown uploads require review before version changes.'}</Text>}
          </Stack>)}
          <Checkbox label="Select an exact compatible modpack release" checked={replacePack} disabled={working} onChange={event => { setReplacePack(event.currentTarget.checked); invalidate(); }} />
          {replacePack && <>
            <SimpleGrid cols={{ base: 1, sm: 2 }}>
              <Select label="Pack publisher" allowDeselect={false} data={['modrinth', 'curseforge']} value={packRequest.pack.provider} disabled={working} onChange={value => { if (value === 'modrinth' || value === 'curseforge') { setPackRequest(current => changePackSource(current, { provider: value })); setVerifiedFiles({}); invalidate(); } }} />
              {(['project_id', 'version_id', 'file_id'] as const).map(field => <TextInput key={field} label={{ project_id: 'Pack project ID', version_id: 'Exact pack release ID', file_id: 'Exact pack file ID' }[field]} required maxLength={256} value={packRequest.pack[field]} disabled={working} onChange={event => { const value = event.currentTarget.value; setPackRequest(current => changePackSource(current, { [field]: value })); setVerifiedFiles({}); invalidate(); }} />)}
            </SimpleGrid>
            <Text size="sm" c="dimmed">Use the publisher’s exact project, release and file IDs. CurseForge requires the administrator’s private API key. Changing the source requires verifying any uploaded files again.</Text>
          </>}
        </>}
        <Button className="app-wrap-label" w="fit-content" loading={busy} disabled={activeJob || blocked || packIncomplete || mode === 'manual' && (contentIncomplete || !minecraft || flavor !== 'vanilla' && !loader)} onClick={() => void prepare()}>Prepare update preview</Button>
        {!!previews.length && <Select label="Prepared previews" placeholder="Choose an unexpired preview" value={preview?.id ?? null} data={previews.map(plan => ({ value: plan.id, label: selection(plan.target) + ' · ' + new Date(plan.expires_at).toLocaleTimeString() }))} disabled={working} onChange={value => choosePreview(previews.find(plan => plan.id === value) ?? null)} />}
      </> : <Text>Operator access is required to prepare or apply updates.</Text>}
    </Stack></Paper>}
    {job && <Paper withBorder p="lg"><Stack gap="xs"><Group><Badge color={job.state === 'failed' ? 'red' : job.state === 'succeeded' ? 'forest' : 'blue'}>{job.state}</Badge><Text>{job.phase || 'Waiting to start'}</Text></Group>{activeJob && <Progress value={job.progress} aria-label="Update progress" />}{job.error && <Text c="red">{job.error}</Text>}{finalDetailsError && <Alert color="yellow" title="Final details incomplete" role="alert">{finalDetailsError}<Button mt="sm" variant="light" loading={finalDetailsLoading} onClick={() => setFinalAttempt(value => value + 1)}>Retry final details</Button></Alert>}<Anchor component={Link} to="/jobs">View job output</Anchor></Stack></Paper>}
    {preview && <Paper withBorder p="lg"><Stack><Group justify="space-between"><Title order={3} size="h4">Review exact changes</Title><Badge color={dirty || expired || preview.blockers.length || packBlocked || Object.keys(preview.content_manual_required ?? {}).length ? 'orange' : 'forest'}>{dirty ? 'Prepare changes again' : expired ? 'Expired' : preview.blockers.length || packBlocked || Object.keys(preview.content_manual_required ?? {}).length ? 'Blocked' : 'Ready for confirmation'}</Badge></Group><Text>{selection(preview.from)} to {selection(preview.target)}</Text><Text size="sm">Managed Java {preview.runtime.major}, {preview.runtime.version}. Preview expires {new Date(preview.expires_at).toLocaleString()}.</Text>
      {dirty && <Alert color="yellow" role="status">Choices have changed. Prepare a new preview before applying. The displayed plan is the previous verified preview.</Alert>}
      {preview.blockers.map((blocker, index) => <Alert key={index} color="yellow">{blocker.message}</Alert>)}{expired && <Alert color="yellow">This preview expired. Prepare another preview before applying.</Alert>}
      {!preview.changes.length && <Text>No executable or content changes were found.</Text>}
      {preview.changes.map((change, index) => <Stack key={index} gap={4} py="xs" style={{ overflowWrap: 'anywhere' }}><Text fw={600}>{change.component}</Text><Text size="sm" c="dimmed">Current: {change.before || 'Absent'}</Text><Text size="sm">Proposed: {change.after || 'Remove'}</Text></Stack>)}
      {!!Object.keys(preview.content_manual_required ?? {}).length && <ContentManualReview required={preview.content_manual_required ?? {}} files={contentFiles} administrator={admin} editable={operator && mode === 'manual'} busy={working} uploadDisabled={dirty || expired} onFile={(id, file) => setContentFiles(current => ({ ...current, [id]: file }))} onUpload={id => void uploadContent(id)} />}
      {preview.pack && <PackReview preview={preview.pack} request={packRequest} editable={operator && mode === 'manual' && replacePack} administrator={admin} busy={working} uploadDisabled={dirty || expired} archiveFile={archiveFile} memberFiles={memberFiles} onRequest={next => { setPackRequest(next); invalidate(); }} onArchiveFile={setArchiveFile} onMemberFile={(path, file) => setMemberFiles(current => ({ ...current, [path]: file }))} onUploadArchive={() => void uploadArchive()} onUploadMember={path => void uploadMember(path)} />}
      {preview.pack && operator && mode === 'manual' && replacePack && <Button className="app-wrap-label" variant="light" disabled={working || expired || packIncomplete || contentIncomplete} onClick={() => void prepare()}>Prepare again with pack choices and verified files</Button>}
      {Object.entries(verifiedFiles).map(([path, receipt]) => <Text key={path} size="sm" style={{ overflowWrap: 'anywhere' }}>{path.startsWith('content:') ? 'Staged content file ' + path.slice(8) + ' (publisher checks require the new preview)' : 'Verified ' + path}: {receipt.size} bytes · SHA256 {receipt.sha256}</Text>)}
      <Divider label="Verified downloads" />{preview.artifacts.map((artifact, index) => <Text key={index} size="sm" style={{ overflowWrap: 'anywhere' }}>{artifact.filename} · {artifact.size} bytes · {artifact.checksum.algorithm}: {artifact.checksum.value}</Text>)}
      <Button w="fit-content" disabled={!canApply} onClick={() => { setConfirmed(false); setApplyDialog(true); }}>Review and apply update</Button>
    </Stack></Paper>}
    {status && <><Stack><Title order={3}>Update history</Title>{!status.history.length && <Text c="dimmed">No update has been applied to this server.</Text>}{status.history.map(item => <Paper key={item.id} withBorder p="md"><Stack gap="sm"><Group justify="space-between"><Text fw={600}>{item.kind === 'instance_rollback' ? 'Confirmed instance rollback' : 'Server update'}</Text><Badge color={item.state === 'blocked' || item.state === 'boot_attempted' ? 'orange' : 'gray'}>{historyLabel(item.state)}</Badge></Group><Text size="sm">{selection(item.from)} to {selection(item.target)}</Text><Text size="sm" c="dimmed">{new Date(item.updated_at).toLocaleString()}</Text>{item.boot_attempted && item.state !== 'complete' && item.state !== 'superseded' && <Text size="sm">The new version may have migrated world data. Choosing an older executable requires a separately confirmed instance rollback.</Text>}<Group wrap="wrap"><Anchor component={Link} to="/jobs">View jobs</Anchor>{admin && item.kind === 'update' && item.boot_attempted && (item.state === 'blocked' || item.state === 'boot_attempted') && <Button color="red" variant="light" disabled={working} onClick={() => void previewRollback(item.id)}>Review instance rollback</Button>}</Group></Stack></Paper>)}</Stack><Text size="sm" c="dimmed">{status.qualification}</Text></>}
    <Modal opened={applyDialog} onClose={() => !busy && setApplyDialog(false)} title="Apply reviewed update" closeButtonProps={{ 'aria-label': 'Close update confirmation' }} size="lg" closeOnEscape={!busy} closeOnClickOutside={!busy}><Stack>{expired && <Alert color="yellow" role="status">This preview expired. Prepare another preview before applying.</Alert>}<Text>{preview ? selection(preview.target) : ''}</Text><Text>A full safety backup must succeed first. Running servers then warn players, stop, commit the reviewed selection and content, and verify the new start. Failed starts retain the new version behind a recovery blocker.</Text><Text size="sm" style={{ overflowWrap: 'anywhere' }}>{preview?.pack ? 'Pack: ' + preview.pack.source.name + ' · release ' + preview.pack.source.selection.version_id + ' · file ' + preview.pack.source.selection.file_id : 'No pack replacement'}</Text><Text size="sm">Warning countdown: {preview?.countdown_seconds} seconds.</Text><Checkbox data-autofocus label="I confirm these exact changes and the warning countdown." checked={confirmed} onChange={event => setConfirmed(event.currentTarget.checked)} /><Button loading={busy} disabled={!confirmed || !canApply} onClick={() => void apply()}>Confirm update</Button><Button variant="default" disabled={busy} onClick={() => setApplyDialog(false)}>Cancel</Button>{error && <Alert color="red" role="alert">{error}</Alert>}</Stack></Modal>
    <Modal opened={rollback !== null} onClose={() => !busy && setRollback(null)} title="Restore instance and world data" closeButtonProps={{ 'aria-label': 'Close instance rollback confirmation' }} size="lg" closeOnEscape={!busy} closeOnClickOutside={!busy}><Stack>{rollbackExpired && <Alert color="yellow" role="status">This rollback preview expired. Close the dialog and review rollback again.</Alert>}<Alert color="red">This explicitly rewinds world data to the update’s full safety backup. Progress since that backup is replaced. A new full backup of the current stopped instance must succeed first.</Alert>{rollback && <><Text>Restore {selection(rollback.target)} from backup {rollback.backup_id}.</Text><Text size="sm" style={{ overflowWrap: 'anywhere' }}>Backup SHA256: {rollback.backup_sha256}</Text></>}<Text>The server remains stopped after restore. External mod and plugin databases are excluded.</Text><TextInput label={'Type ' + instanceId + ' to confirm instance rollback'} value={rollbackConfirmation} onChange={event => setRollbackConfirmation(event.currentTarget.value)} autoComplete="off" data-autofocus /><Checkbox label="I explicitly authorize restoring this backup, including its world data." checked={rewindAcknowledged} onChange={event => setRewindAcknowledged(event.currentTarget.checked)} /><Button color="red" loading={busy} disabled={!rewindAcknowledged || rollbackConfirmation !== instanceId || rollbackExpired} onClick={() => void applyRollback()}>Confirm instance rollback</Button><Button variant="default" disabled={busy} onClick={() => setRollback(null)}>Cancel</Button>{error && <Alert color="red" role="alert">{error}</Alert>}</Stack></Modal>
  </Stack>;
}

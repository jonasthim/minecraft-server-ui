import { useCallback, useEffect, useRef, useState } from 'react';
import { Alert, Anchor, Badge, Button, Checkbox, Group, Loader, Modal, Paper, Select, Stack, Text, Title } from '@mantine/core';
import { Link, useParams } from 'react-router-dom';
import { api } from '../../api/client';
import type { components } from '../../api/schema';
import { useAuth } from '../auth/AuthProvider';

type Backup = components['schemas']['BackupRecord'];
type Job = components['schemas']['Job'];
type Kind = components['schemas']['BackupCreateRequest']['kind'];
const message = (error: unknown) => error instanceof Error ? error.message : 'Backup operation failed.';
const bytes = (value: number) => value < 1024 * 1024 ? (value / 1024).toFixed(1) + ' KiB' : value < 1024 ** 3 ? (value / 1024 ** 2).toFixed(1) + ' MiB' : (value / 1024 ** 3).toFixed(2) + ' GiB';

export function BackupsPage() {
  const { instanceId = '' } = useParams();
  return <Stack gap="lg"><Anchor component={Link} to={'/instances/' + encodeURIComponent(instanceId)}>Back to server</Anchor><BackupsPanel key={instanceId} instanceId={instanceId} /></Stack>;
}

export function BackupsPanel({ instanceId }: { instanceId: string }) {
  const { user } = useAuth();
  const canOperate = user?.role === 'operator' || user?.role === 'admin';
  const endpoint = '/instances/' + encodeURIComponent(instanceId) + '/backups';
  const [backups, setBackups] = useState<Backup[]>([]);
  const [kind, setKind] = useState<Kind>('world');
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [job, setJob] = useState<Job | null>(null);
  const [restore, setRestore] = useState<Backup | null>(null);
  const [confirmed, setConfirmed] = useState(false);
  const frame = useRef<HTMLIFrameElement>(null);
  const frameName = 'backup-download-' + instanceId;
  const activeJob = job?.state === 'queued' || job?.state === 'running';
  const load = useCallback(async (signal?: AbortSignal) => {
    try { const response = await api<components['schemas']['BackupListResponse']>('/instances/' + encodeURIComponent(instanceId) + '/backups', { signal }); if (!signal?.aborted) setBackups(response.backups); }
    catch (error) { if (!signal?.aborted) setError(message(error)); }
    finally { if (!signal?.aborted) setLoading(false); }
  }, [instanceId]);
  useEffect(() => { const controller = new AbortController(); void load(controller.signal); return () => controller.abort(); }, [load]);
  useEffect(() => {
    if (!activeJob || !job?.id) return;
    const controller = new AbortController();
    const poll = async () => {
      try {
        const response = await api<components['schemas']['BackupJobResponse']>('/jobs/' + job.id, { signal: controller.signal });
        if (controller.signal.aborted) return;
        if (response.job.state === 'queued' || response.job.state === 'running') { setJob(response.job); return; }
        // Refresh before closing the active-job effect; setting a terminal job
        // first would abort this effect's own metadata request during cleanup.
        let failure = '';
        if (response.job.state === 'failed' || response.job.state === 'cancelled') { const output = await api<{ logs: components['schemas']['JobLog'][] }>('/jobs/' + job.id + '/logs?after=0&limit=100', { signal: controller.signal }); failure = output.logs.at(-1)?.message || response.job.error || 'Operation did not complete. Inspect recovery status and job output before retrying.'; }
        await load(controller.signal);
        if (controller.signal.aborted) return;
        setJob(response.job);
        if (failure) { setNotice(''); setError(failure); }
        else setNotice(response.job.kind === 'backup_restore' ? 'Restore completed. The server remains stopped.' : 'Backup validated and ready.');
      } catch (error) { if (!controller.signal.aborted) setError('Could not check operation result. ' + message(error)); }
    };
    const timer = window.setInterval(() => { void poll(); }, 1000);
    return () => { controller.abort(); window.clearInterval(timer); };
  }, [activeJob, job?.id, load]);
  const create = async () => {
    setBusy(true); setError(''); setNotice('');
    try { const input: components['schemas']['BackupCreateRequest'] = { kind }; const response = await api<components['schemas']['BackupJobResponse']>(endpoint, { method: 'POST', body: JSON.stringify(input) }); setJob(response.job); setNotice('Backup queued. Saving will be re-enabled before the archive is compressed.'); }
    catch (error) { setError(message(error)); } finally { setBusy(false); }
  };
  const restoreBackup = async () => {
    if (!restore || !confirmed) return;
    setBusy(true); setError(''); setNotice('');
    try { const input: components['schemas']['BackupRestoreRequest'] = { confirmed: true }; const response = await api<components['schemas']['BackupJobResponse']>(endpoint + '/' + restore.id + '/restore', { method: 'POST', body: JSON.stringify(input) }); setJob(response.job); setRestore(null); setConfirmed(false); setNotice('Confirmed restore queued. A stopped server and successful safety backup are required.'); }
    catch (error) { setError(message(error)); } finally { setBusy(false); }
  };
  const downloadResult = () => {
    const doc = frame.current?.contentDocument;
    if (doc?.contentType !== 'application/json') return;
    try { const response: components['schemas']['ErrorResponse'] = JSON.parse(doc.body.textContent ?? ''); setNotice(''); setError(response.error.message); }
    catch { setNotice(''); setError('Archive download failed. Refresh and try again.'); }
  };
  return <Stack gap="md" style={{ minWidth: 0 }}>
    <Group justify="space-between" wrap="wrap"><Title order={2}>Backups</Title><Button variant="default" loading={loading} onClick={() => { setError(''); void load(); }}>Refresh backups</Button></Group>
    {error && <Alert color="red" role="alert">{error}</Alert>}
    {notice && <Alert color={activeJob ? 'blue' : 'green'} role="status">{notice}</Alert>}
    {job && <Text size="sm">Operation: {job.state} · {job.phase.replaceAll('_', ' ')} · <Anchor component={Link} to="/jobs">View jobs and output</Anchor></Text>}
    <Alert color="yellow" title="Consistency and scope">Online backups flush native world saves without stopping the server. Player and mod/plugin data may still change. External databases are excluded. Full backups include local addon data and configuration; downloaded archives can contain addon credentials and must be kept private.</Alert>
    <Text size="sm" c="dimmed">Automatic backups retain at most 10 archives for 30 days. Manual and safety backups are protected from automatic retention.</Text>
    {canOperate && <Paper withBorder p="md"><Stack><Select label="Backup scope" value={kind} onChange={value => { if (value === 'world' || value === 'full') setKind(value); }} data={[{ value: 'world', label: 'World and associated dimensions' }, { value: 'full', label: 'Full local server data and addons' }]} /><Button loading={busy} disabled={activeJob} onClick={() => { void create(); }}>Create protected manual backup</Button></Stack></Paper>}
    {loading && <Loader aria-label="Loading backups" />}
    {!loading && backups.length === 0 && <Paper withBorder p="md"><Text>No backups yet. An operator can create a backup after the server has saved a world, or create a full backup of local server data.</Text></Paper>}
    {backups.map(backup => <Paper key={backup.id} withBorder p="md" style={{ minWidth: 0 }}><Stack gap="sm">
      <Group justify="space-between" wrap="wrap"><Text fw={600}>{backup.kind === 'world' ? 'World backup' : 'Full backup'} · {backup.trigger}</Text><Group gap="xs"><Badge color={backup.state === 'ready' ? 'green' : backup.state === 'failed' ? 'red' : 'yellow'}>{backup.state === 'ready' ? 'Validated' : backup.state}</Badge>{backup.protected && <Badge variant="light">Protected</Badge>}</Group></Group>
      <Text size="sm">Created {new Date(backup.created_at).toLocaleString()} · {bytes(backup.size_bytes)}</Text>
      {backup.validated_at && <Text size="xs" c="dimmed">Validated {new Date(backup.validated_at).toLocaleString()}</Text>}
      {backup.recovery && <Alert color="orange" title="Recovery status">{backup.recovery}</Alert>}
      {backup.warnings.map(warning => <Text size="xs" c="dimmed" key={warning}>{warning}</Text>)}
      <Text size="xs" style={{ overflowWrap: 'anywhere' }}>Archive {backup.id} · Job {backup.job_id}</Text>
      {canOperate && backup.state === 'ready' && <Group wrap="wrap"><Button component="a" variant="light" href={'/api/v1' + endpoint + '/' + backup.id + '/download'} target={frameName} onClick={() => { setError(''); setNotice('Validating the private archive for download. Check your browser downloads for progress.'); }}>Download ZIP</Button><Button variant="outline" color="orange" disabled={activeJob || busy} onClick={() => { setRestore(backup); setConfirmed(false); }}>Restore…</Button></Group>}
    </Stack></Paper>)}
    <Modal opened={restore !== null} onClose={() => { if (!busy) setRestore(null); }} title="Restore saved data" centered>
      <Stack><Alert color="orange">This replaces {restore?.kind === 'world' ? 'the saved world and associated dimensions' : 'local server saved data, addons and configuration'} with the selected archive. The server must already be stopped. A new protected safety backup must succeed before any data is replaced.</Alert><Text size="sm">The archive must match the current Minecraft version, flavor, loader, world name and layout. Current manager settings, ports, EULA acceptance, runtime and private management credentials are preserved. Server startup is blocked if replacement is interrupted.</Text><Checkbox label="I confirm replacing the selected saved data with this backup." checked={confirmed} onChange={event => setConfirmed(event.currentTarget.checked)} /><Button color="orange" loading={busy} disabled={!confirmed || activeJob} onClick={() => { void restoreBackup(); }}>Create safety backup and restore</Button></Stack>
    </Modal>
    <iframe hidden name={frameName} ref={frame} title="Backup download response" onLoad={downloadResult} onError={() => { setNotice(''); setError('Could not connect to the archive download.'); }} />
  </Stack>;
}

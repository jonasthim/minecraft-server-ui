import { useCallback, useEffect, useState } from 'react';
import { Alert, Anchor, Badge, Button, Checkbox, Group, Loader, Modal, NumberInput, Paper, Progress, Select, Stack, Text, TextInput, Title } from '@mantine/core';
import { Link } from 'react-router-dom';
import { api } from '../../api/client';
import type { components } from '../../api/schema';
import { useAuth } from '../auth/AuthProvider';

type Detail = components['schemas']['InstanceDetail'];
type State = components['schemas']['LifecycleState'];
type Request = components['schemas']['LifecycleRequest'];
type Preview = components['schemas']['LifecyclePreview'];
type Catalog = components['schemas']['LifecycleCatalog'];
type Job = components['schemas']['Job'];
const message = (error: unknown) => error instanceof Error ? error.message : 'Request failed. Try again.';
const active = (job: Job) => job.state === 'queued' || job.state === 'running';

export function LifecyclePanel({ detail, onChanged }: { detail: Detail; onChanged: () => Promise<void> }) {
  const { user } = useAuth();
  const id = detail.instance.id; const path = '/instances/' + encodeURIComponent(id) + '/lifecycle';
  const operator = user?.role === 'operator' || user?.role === 'admin'; const admin = user?.role === 'admin';
  const [state, setState] = useState<State | null>(null); const [error, setError] = useState(''); const [busy, setBusy] = useState(false);
  const [job, setJob] = useState<Job | null>(null); const [dialog, setDialog] = useState<Request['operation'] | null>(null);
  const [preview, setPreview] = useState<Preview | null>(null); const [catalog, setCatalog] = useState<Catalog | null>(null);
  const [loader, setLoader] = useState(detail.instance.loader_version ?? ''); const [countdown, setCountdown] = useState(300);
  const [confirmation, setConfirmation] = useState(''); const [emergency, setEmergency] = useState(false);
  const refresh = useCallback(async (signal?: AbortSignal) => { const value = await api<State>(path, { signal }); setState(value); }, [path]);
  useEffect(() => { const controller = new AbortController(); const run = () => void refresh(controller.signal).catch(error => { if (!controller.signal.aborted) setError(message(error)); }); run(); const timer = window.setInterval(run, 5000); return () => { controller.abort(); window.clearInterval(timer); }; }, [refresh]);
  useEffect(() => {
    if (!job || !active(job)) return;
    const controller = new AbortController(); const jobID = job.id;
    const timer = window.setInterval(() => { void api<{ job: Job }>('/jobs/' + encodeURIComponent(jobID), { signal: controller.signal }).then(value => { if (!controller.signal.aborted) setJob(value.job); }).catch(error => { if (!controller.signal.aborted) setError(message(error)); }); }, 2000);
    return () => { controller.abort(); window.clearInterval(timer); };
  }, [job?.id, job?.state]);
  useEffect(() => {
    if (!job || active(job)) return;
    const controller = new AbortController();
    // Terminal feedback is already visible. These ancillary reads have their own
    // lifetime and both run even if one fails, so the parent's persisted pin is
    // refreshed independently of status polling.
    void Promise.allSettled([refresh(controller.signal), onChanged()]).then(results => {
      if (controller.signal.aborted) return;
      const failures = results.flatMap((result, index) => result.status === 'rejected' ? [(index === 0 ? 'Server status refresh failed: ' : 'Server details refresh failed: ') + message(result.reason)] : []);
      if (failures.length) setError(failures.join(' '));
    });
    return () => controller.abort();
  }, [job?.id, job?.state, refresh, onChanged]);
  function open(operation: Request['operation']) { setDialog(operation); setError(''); setPreview(null); setConfirmation(''); setCountdown(300); setEmergency(false); }
  async function loadCatalog() {
    setBusy(true); setError('');
    try { const query = new URLSearchParams({ flavor: detail.instance.flavor, minecraft_version: detail.instance.minecraft_version }); setCatalog(await api<Catalog>('/lifecycle/catalog?' + query)); }
    catch (error) { setError(message(error)); } finally { setBusy(false); }
  }
  async function resolve() {
    setBusy(true); setError('');
    try { const selection: components['schemas']['LifecycleSelection'] = { flavor: detail.instance.flavor, minecraft_version: detail.instance.minecraft_version, ...(loader ? { loader_version: loader } : {}) }; setPreview(await api<Preview>(path + '/preview', { method: 'POST', body: JSON.stringify(selection) })); }
    catch (error) { setError(message(error)); } finally { setBusy(false); }
  }
  async function operate(request: Omit<Request, 'countdown_seconds' | 'emergency'> & Partial<Pick<Request, 'countdown_seconds' | 'emergency'>>) {
    setBusy(true); setError('');
    try { const result = await api<components['schemas']['LifecycleJobResponse']>(path, { method: 'POST', body: JSON.stringify({ countdown_seconds: 300, emergency: false, ...request } satisfies Request) }); setJob(result.job); setDialog(null); await refresh(); }
    catch (error) { setError(message(error)); } finally { setBusy(false); }
  }
  const working = busy || !!job && active(job); const stopped = state?.process === 'stopped' || state?.process === 'crashed';
  return <Paper withBorder p="lg"><Stack>
    <Group justify="space-between" wrap="wrap"><Title order={3} size="h4">Server operations</Title><Button size="xs" variant="subtle" onClick={() => void refresh().catch(error => setError(message(error)))}>Refresh status</Button></Group>
    {!state && <Loader size="sm" aria-label="Loading server operations" />}
    {state && <><Group gap="sm" wrap="wrap"><Badge color={state.installed ? 'forest' : 'gray'}>{state.installed ? 'Installed' : 'Not installed'}</Badge><Text size="sm">JVM: {state.process}{state.process_running ? ' · process running' : ''}</Text><Text size="sm">Game readiness: {state.readiness}</Text></Group>
      <Text size="sm" c="dimmed">Process start and game readiness are verified separately. Gameplay overrides can affect native settings.</Text>
      <Text size="sm">Autostart desired: {state.autostart_desired ? 'enabled' : 'disabled'} · Native policy: {state.autostart_applied === null ? 'unknown' : state.autostart_applied ? 'enabled' : 'disabled'}</Text>
      {state.autostart_error && <Alert color="yellow">{state.autostart_error}</Alert>}
      {operator && <Group wrap="wrap"><Button disabled={working || !stopped} variant="light" onClick={() => open('install')}>{state.installed ? 'Reinstall pinned version' : 'Install server files'}</Button><Button disabled={working || !state.installed || !stopped || !detail.eula.accepted} onClick={() => void operate({ operation: 'start' })}>Start server</Button><Button disabled={working || !state.process_running} variant="light" onClick={() => open('stop')}>Stop server</Button><Button disabled={working || !state.installed || !state.process_running || !detail.eula.accepted} variant="light" onClick={() => open('restart')}>Restart server</Button></Group>}
      {operator && !detail.eula.accepted && <Text size="sm">An administrator must explicitly accept the Minecraft EULA in Edit settings before start or autostart.</Text>}
      {admin && <Group wrap="wrap"><Button disabled={working || !state.installed && !state.autostart_desired || !detail.eula.accepted && !state.autostart_desired} variant="default" onClick={() => void operate({ operation: 'autostart', enabled: !state.autostart_desired })}>{state.autostart_desired ? 'Disable autostart' : 'Enable autostart'}</Button><Button disabled={working || !state.installed || !stopped} color="red" variant="light" onClick={() => open('uninstall')}>Uninstall server files</Button></Group>}
    </>}
    {job && <Stack gap="xs"><Group wrap="wrap"><Text size="sm" style={{ overflowWrap: 'anywhere' }}>Job {job.id}</Text><Badge color={job.state === 'failed' ? 'red' : job.state === 'succeeded' ? 'green' : 'blue'}>{job.state}</Badge></Group><Text size="sm">{job.phase || 'Waiting to start'}</Text>{active(job) && <Progress value={job.progress} aria-label="Server operation progress" />}{job.error && <Alert color="red" role="alert">{job.error}</Alert>}<Anchor component={Link} to="/jobs">View durable job progress and output</Anchor></Stack>}
    {error && !dialog && <Alert color="red" role="alert">{error}</Alert>}
    <Modal opened={dialog !== null} onClose={() => !busy && setDialog(null)} title={dialog === 'install' ? 'Install exact server selection' : dialog === 'uninstall' ? 'Uninstall active server files' : dialog === 'restart' ? 'Restart server' : 'Stop server'} size="lg" closeOnEscape={!busy} closeOnClickOutside={!busy}>
      <Stack>
        {dialog === 'install' && <><Text>Minecraft {detail.instance.minecraft_version} · {detail.instance.flavor}. This version is pinned. Installation downloads verified software without launching Minecraft.</Text>
          {!detail.instance.loader_version && detail.instance.flavor !== 'vanilla' && <><Button variant="light" loading={busy} onClick={() => void loadCatalog()}>Load official builds and loaders</Button>{catalog && <><Select label={detail.instance.flavor === 'paper' ? 'Paper build' : 'Loader version'} searchable data={catalog.loaders} value={loader || null} onChange={value => { setLoader(value ?? ''); setPreview(null); }} /><Text size="sm" c="dimmed">{catalog.qualification}</Text></>}</>}
          {detail.instance.loader_version && <Text size="sm">Pinned build/loader: {detail.instance.loader_version}</Text>}
          <Button variant="light" loading={busy} disabled={!detail.instance.loader_version && detail.instance.flavor !== 'vanilla' && !loader} onClick={() => void resolve()}>Resolve verified install preview</Button>
          {preview && <><Text fw={600}>Exact selection: {preview.selection.minecraft_version}{preview.selection.loader_version ? ' / ' + preview.selection.loader_version : ''}</Text><Text size="sm">Managed Java {preview.requirements.java_major}; preview expires {new Date(preview.expires_at).toLocaleString()}.</Text>{preview.artifacts.map((artifact, index) => <Text key={index} size="sm" style={{ overflowWrap: 'anywhere' }}>{artifact.filename} · {artifact.size} bytes · {artifact.checksum.algorithm}: {artifact.checksum.value}</Text>)}{preview.warnings.map((warning, index) => <Alert key={index} color="yellow">{warning}</Alert>)}<Button loading={busy} onClick={() => void operate({ operation: 'install', preview_id: preview.id })}>Confirm installation</Button></>}
        </>}
        {(dialog === 'stop' || dialog === 'restart') && <><Text>The server must acknowledge player observation and warning announcements before a normal stop. A cancelled countdown leaves the server running.</Text><NumberInput label="Warning countdown (seconds)" description="Default: five minutes." min={0} max={3600} allowDecimal={false} disabled={emergency} value={countdown} onChange={value => setCountdown(Number(value))} />{admin && <Checkbox label="Emergency stop if console verification is unavailable" checked={emergency} onChange={event => setEmergency(event.currentTarget.checked)} />}{emergency && <><Alert color="yellow">Emergency stop skips announcements and countdown. Native graceful shutdown and bounded forced termination still apply.</Alert><TextInput label={'Type ' + id + ' to confirm emergency stop'} value={confirmation} onChange={event => setConfirmation(event.currentTarget.value)} autoComplete="off" /></>}<Button color="orange" loading={busy} disabled={emergency && confirmation !== id} onClick={() => void operate({ operation: dialog, countdown_seconds: countdown, emergency, confirmation })}>{dialog === 'restart' ? 'Confirm restart' : 'Confirm stop'}</Button></>}
        {dialog === 'uninstall' && <><Text>Disable autostart and remove this server’s active launch. Worlds, settings, mods/plugins, backups, managed Java, and immutable executable generations are retained. The server must be stopped.</Text><TextInput label={'Type ' + id + ' to confirm uninstall'} value={confirmation} onChange={event => setConfirmation(event.currentTarget.value)} autoComplete="off" data-autofocus /><Button color="red" loading={busy} disabled={confirmation !== id} onClick={() => void operate({ operation: 'uninstall', confirmation })}>Confirm uninstall</Button></>}
        {error && <Alert color="red" role="alert">{error}</Alert>}
        <Button variant="default" disabled={busy} onClick={() => setDialog(null)}>Cancel</Button>
      </Stack>
    </Modal>
  </Stack></Paper>;
}

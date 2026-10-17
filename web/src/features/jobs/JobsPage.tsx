import { useCallback, useEffect, useRef, useState } from 'react';
import { Alert, Badge, Button, Code, Group, Loader, Paper, Progress, Stack, Text, Title, Select, TextInput, Drawer } from '@mantine/core';
import { Link, useSearchParams } from 'react-router-dom';
import { api } from '../../api/client';
import type { components } from '../../api/schema';
import { useAuth } from '../auth/AuthProvider';
import { startMetricsPoll } from '../metrics/poll';

type Job = components['schemas']['Job'];
type JobLog = components['schemas']['JobLog'];
type JobEvent = components['schemas']['JobEvent'];
const active = (job: Job) => job.state === 'queued' || job.state === 'running';
const color = (job: Job) => ({ queued: 'gray', running: 'blue', succeeded: 'green', failed: 'red', cancelled: 'gray' })[job.state];
const message = (error: unknown) => error instanceof Error ? error.message : 'Request failed';
const mergeLogs = (previous: JobLog[], incoming: JobLog[]) => [...new Map([...previous, ...incoming].map(line => [line.id, line])).values()].sort((a, b) => a.id - b.id).slice(-500);

export const jobHref = (jobID: string) => '/jobs?job=' + encodeURIComponent(jobID);

/** Keyed by account/role so requests, output and mutation state cannot cross sessions. */
export function JobsPage({ instanceID }: { instanceID?: string }) {
  const { user } = useAuth();
  return <JobsView key={String(user?.id) + ':' + user?.role + ':' + instanceID} instanceID={instanceID} />;
}
function JobsView({ instanceID }: { instanceID?: string }) {
  const [search, setSearch] = useSearchParams();
  const selected = search.get('job');
  const [jobs, setJobs] = useState<Job[]>([]);
  const [state, setState] = useState('');
  const [instance, setInstance] = useState(instanceID || '');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => startMetricsPoll(signal => api<{ jobs: Job[] }>('/jobs' + (instanceID ? '?instance_id=' + encodeURIComponent(instanceID) : ''), { signal: AbortSignal.any([signal, AbortSignal.timeout(10000)]) }), result => { setJobs(result.jobs); setError(''); setLoading(false); }, error => { setError(message(error)); setLoading(false); }, 10000), [instanceID, attempt]);
  const visible = jobs.filter(job => (!state || job.state === state) && (!instance || job.instance_id === instance));
  const select = (id: string | null) => { const next = new URLSearchParams(search); if (id) next.set('job', id); else next.delete('job'); setSearch(next); };
  return <Stack>
    <Group justify="space-between" wrap="wrap"><div><Title order={2}>Jobs</Title><Text c="dimmed">Installation, backups, and other operations keep their progress here.</Text></div><Button variant="light" onClick={() => setAttempt(value => value + 1)}>Refresh jobs</Button></Group>
    <Group grow align="start"><Select label="Job state" clearable placeholder="All states" value={state || null} onChange={value => setState(value || '')} data={['queued', 'running', 'succeeded', 'failed', 'cancelled']} /><TextInput label="Exact instance ID" disabled={!!instanceID} value={instance} onChange={event => setInstance(event.currentTarget.value)} placeholder="All instances" /></Group>
    <Text size="sm" c="dimmed">Showing {visible.length} of {jobs.length} loaded recent jobs (up to 100). Filters apply to this loaded history only; older jobs can be opened by their exact link.</Text>
    {error && <Alert color="red" role="alert">{error}{jobs.length > 0 ? ' Showing the last loaded history; it may be stale.' : ''}</Alert>}
    {loading && <Loader aria-label="Loading jobs" />}
    {!loading && !error && visible.length === 0 && <Paper withBorder p="lg"><Text>{jobs.length ? 'No loaded jobs match these filters.' : 'No jobs yet. Start an operation from an instance to see its progress here.'}</Text></Paper>}
    {visible.map(job => <Paper key={job.id} withBorder p="md"><Group justify="space-between" align="start" wrap="wrap"><Stack gap={4} style={{ minWidth: 0, flex: 1 }}><Text fw={600} style={{ overflowWrap: 'anywhere' }}>{job.kind}{job.instance_id ? ' · ' + job.instance_id : ''}</Text><Text size="sm" c="dimmed">{new Date(job.created_at).toLocaleString()}</Text><Text size="sm">{job.phase || 'Waiting to start'}</Text></Stack><Badge color={color(job)}>{job.cancel_requested && active(job) ? 'Cancelling' : job.state}</Badge><Button variant="light" onClick={() => select(job.id)}>Details</Button></Group>{active(job) && <Progress mt="sm" value={job.progress} aria-label={job.kind + ' progress'} />}</Paper>)}
    <Drawer opened={!!selected} onClose={() => select(null)} title="Job activity" position="right" size="lg">{selected && <JobDetails key={selected} jobID={selected} onUpdate={next => setJobs(current => current.map(item => item.id === next.id ? next : item))} />}</Drawer>
  </Stack>;
}

/** Root may mount this inside the authenticated shell. No props or shared wiring required. */
export function JobActivity() {
  const { user } = useAuth();
  return <Activity key={String(user?.id) + ':' + user?.role} />;
}
function Activity() {
  const [jobs, setJobs] = useState<Job[]>([]); const [error, setError] = useState(''); const [opened, setOpened] = useState(false); const [selected, setSelected] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);
  useEffect(() => startMetricsPoll(signal => api<{ jobs: Job[] }>('/jobs', { signal: AbortSignal.any([signal, AbortSignal.timeout(10000)]) }), result => { setJobs(result.jobs); setError(''); setLoaded(true); }, error => setError(message(error)), 10000), []);
  return <><Button variant="subtle" styles={{ label: { whiteSpace: 'normal' } }} onClick={() => setOpened(true)}>Activity{error ? ' · unavailable' : loaded ? ' · ' + jobs.filter(active).length + ' active' : ' · loading…'}</Button><Drawer opened={opened} onClose={() => { setOpened(false); setSelected(null); }} title="Recent job activity" position="right" size="lg"><Stack>{error && <Alert color="red">{error}{loaded ? ' Previously loaded jobs may be stale.' : ' Recent job activity is unknown.'}</Alert>}<Button component={Link} to={selected ? jobHref(selected) : '/jobs'} onClick={() => setOpened(false)} variant="light">Open jobs page</Button>{selected ? <JobDetails key={selected} jobID={selected} onUpdate={next => setJobs(current => current.map(job => job.id === next.id ? next : job))} /> : <><Text size="sm">Up to 100 recent authorized jobs. This is not a complete activity history.</Text>{jobs.slice(0, 10).map(job => <Button key={job.id} variant="light" onClick={() => setSelected(job.id)} styles={{ label: { whiteSpace: 'normal', overflowWrap: 'anywhere' } }}>{job.kind} · {job.instance_id || 'Manager'} · {job.state}</Button>)}{!loaded && !error && <Text>Loading recent job activity…</Text>}{loaded && !jobs.length && !error && <Text>No recent jobs loaded.</Text>}</>}</Stack></Drawer></>;
}
export function JobDetails({ jobID, onUpdate }: { jobID: string; onUpdate: (job: Job) => void }) {
  const { user } = useAuth();
  return <JobDetailContent key={jobID + ':' + user?.id + ':' + user?.role} jobID={jobID} onUpdate={onUpdate} />;
}
function JobDetailContent({ jobID, onUpdate }: { jobID: string; onUpdate: (job: Job) => void }) {
  const { user } = useAuth();
  const [job, setJob] = useState<Job | null>(null);
  const [logs, setLogs] = useState<JobLog[]>([]);
  const [error, setError] = useState('');
  const [connection, setConnection] = useState('Connecting');
  const [cancelling, setCancelling] = useState(false);
  const [notice, setNotice] = useState('');
  const [attempt, setAttempt] = useState(0);
  const onUpdateRef = useRef(onUpdate); onUpdateRef.current = onUpdate;
  const update = useCallback((next: Job) => { setJob(next); onUpdateRef.current(next); }, []);
  useEffect(() => {
    const controller = new AbortController();
    let stream: EventSource | null = null;
    let refreshing = false;
    let pendingRefresh = false;
    let terminal = false;
    let cursor = 0;
    setConnection('Loading saved progress'); setError(''); setJob(null); setLogs([]); setNotice('');
    const ingest = (incoming: JobLog[]) => { if (!controller.signal.aborted && incoming.length) setLogs(current => mergeLogs(current, incoming)); };
    const finish = () => {
      terminal = true;
      stream?.close(); stream = null;
      setConnection('Job finished; loading final output');
    };
    // Only durable log pages advance the cursor. Live events can arrive after a
    // gap, so advancing from their IDs could skip saved output during recovery.
    const refresh = async () => {
      if (refreshing) { pendingRefresh = true; return; }
      refreshing = true;
      do {
        pendingRefresh = false;
        try {
          const value = await api<{ job: Job }>('/jobs/' + encodeURIComponent(jobID), { signal: AbortSignal.any([controller.signal, AbortSignal.timeout(10000)]) });
          if (controller.signal.aborted) return;
          if (!active(value.job)) finish();
          if (!terminal || !active(value.job)) update(value.job);
          for (let page = 0; page < 20; page++) {
            const value = await api<{ logs: JobLog[] }>('/jobs/' + encodeURIComponent(jobID) + '/logs?after=' + cursor + '&limit=500', { signal: AbortSignal.any([controller.signal, AbortSignal.timeout(10000)]) });
            ingest(value.logs);
            if (value.logs.length) cursor = value.logs[value.logs.length - 1].id;
            if (value.logs.length < 500) break;
          }
          if (controller.signal.aborted) return;
          setError('');
          if (terminal && !pendingRefresh) setConnection('Job finished; saved output loaded');
          if (!terminal && !stream) connect();
        } catch (error) {
          if (!controller.signal.aborted) {
            setError(message(error));
            setConnection(terminal ? 'Job finished; final output could not be loaded' : 'Saved progress unavailable; retry to reconnect');
          }
        }
      } while (pendingRefresh && !controller.signal.aborted);
      refreshing = false;
    };
    const connect = () => {
      stream = new EventSource('/api/v1/jobs/' + encodeURIComponent(jobID) + '/events');
      setConnection('Connecting; saved progress shown');
      stream.onopen = () => { if (!terminal) { setConnection('Live'); void refresh(); } };
      stream.onerror = () => {
        if (!terminal) { setConnection('Reconnecting; progress may be delayed. Recovering saved output.'); void refresh(); }
      };
      stream.addEventListener('progress', event => {
        try {
          const value = JSON.parse((event as MessageEvent<string>).data) as JobEvent;
          if (value.job) {
            update(value.job);
            if (!active(value.job)) { finish(); void refresh(); }
          }
        } catch { setError('Could not read job progress. Retry to reload saved progress.'); }
      });
      stream.addEventListener('log', event => {
        try { const value = JSON.parse((event as MessageEvent<string>).data) as JobEvent; if (value.log) ingest([value.log]); }
        catch { setError('Could not read live output. Retry to recover saved output or download the log.'); }
      });
    };
    void refresh();
    return () => { controller.abort(); stream?.close(); };
  }, [jobID, update, attempt]);
  const canCancel = !!job && active(job) && job.cancellable && !job.cancel_requested && (user?.role === 'admin' || user?.role === 'operator' && user.id === job.requested_by);
  async function cancel() {
    setCancelling(true); setError(''); setNotice('');
    try { const value = await api<{ job: Job }>('/jobs/' + encodeURIComponent(jobID) + '/cancel', { method: 'POST' }); update(value.job); setNotice(value.job.state === 'cancelled' ? 'Job cancelled.' : 'Cancellation requested. Waiting for the operation to finish cleanup.'); }
    catch (error) { setError(message(error)); }
    finally { setCancelling(false); }
  }
  return <Stack mt="lg" gap="sm">
    <Title order={3} size="h4">Job details</Title>
    <Text size="xs" c="dimmed" style={{ overflowWrap: 'anywhere' }}>{jobID}</Text>
    <Text size="sm" role="status">{connection}</Text>
    {error && <Alert color="red" role="alert">{error}</Alert>}
    {notice && <Alert color="green" role="status">{notice}</Alert>}
    {job?.error && <Alert color={job.state === 'failed' ? 'red' : 'gray'}>{job.error}</Alert>}
    {job && <Text size="sm" style={{ overflowWrap: 'anywhere' }}>{job.kind} · {job.instance_id || 'Manager'} · {job.state} · {job.phase || 'Queued'} · {job.progress}%{job.cancel_requested && active(job) ? ' · Waiting for cancellation cleanup' : ''}</Text>}
    <Group wrap="wrap"><Button variant="light" onClick={() => setAttempt(value => value + 1)}>Reload saved progress</Button><Button component="a" disabled={!job} variant="light" href={'/api/v1/jobs/' + jobID + '/logs/download'}>Download log</Button>{canCancel && <Button color="red" variant="light" loading={cancelling} onClick={() => void cancel()}>Cancel job</Button>}</Group>
    <Text size="xs" c="dimmed">Showing the newest 500 lines. The download and paginated API include earlier output.</Text>
    <Code block tabIndex={0} role="region" aria-label="Job output" style={{ maxHeight: 320, overflow: 'auto', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{logs.length ? logs.map(line => new Date(line.created_at).toLocaleTimeString() + ' ' + line.message).join('\n') : 'No output yet.'}</Code>
  </Stack>;
}

import { useCallback, useEffect, useRef, useState } from 'react';
import { Alert, Badge, Button, Code, Group, Loader, Paper, Progress, Stack, Text, Title } from '@mantine/core';
import { api } from '../../api/client';
import type { components } from '../../api/schema';
import { useAuth } from '../auth/AuthProvider';

type Job = components['schemas']['Job'];
type JobLog = components['schemas']['JobLog'];
type JobEvent = components['schemas']['JobEvent'];
const active = (job: Job) => job.state === 'queued' || job.state === 'running';
const color = (job: Job) => ({ queued: 'gray', running: 'blue', succeeded: 'green', failed: 'red', cancelled: 'gray' })[job.state];
const message = (error: unknown) => error instanceof Error ? error.message : 'Request failed';
const mergeLogs = (previous: JobLog[], incoming: JobLog[]) => [...new Map([...previous, ...incoming].map(line => [line.id, line])).values()].sort((a, b) => a.id - b.id).slice(-500);

/** Mount inside the authenticated shell at /jobs; optionally scope to an instance. */
export function JobsPage({ instanceID }: { instanceID?: string }) {
  const [jobs, setJobs] = useState<Job[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const refresh = useCallback(async (signal?: AbortSignal) => {
    const result = await api<{ jobs: Job[] }>('/jobs' + (instanceID ? '?instance_id=' + encodeURIComponent(instanceID) : ''), { signal });
    setJobs(result.jobs); setError(''); setLoading(false);
  }, [instanceID]);
  useEffect(() => {
    const controller = new AbortController(); setLoading(true);
    const load = () => void refresh(controller.signal).catch(error => { if (!controller.signal.aborted) { setError(message(error)); setLoading(false); } });
    load(); const timer = window.setInterval(load, 10000);
    return () => { controller.abort(); window.clearInterval(timer); };
  }, [refresh]);
  return <Stack>
    <Group justify="space-between"><div><Title order={2}>Jobs</Title><Text c="dimmed">Installation, backups, and other operations keep their progress here.</Text></div><Button variant="light" onClick={() => void refresh().catch(error => setError(message(error)))}>Refresh jobs</Button></Group>
    {error && <Alert color="red" role="alert">{error}</Alert>}
    {loading && <Loader aria-label="Loading jobs" />}
    {!loading && jobs.length === 0 && <Paper withBorder p="lg"><Text>No jobs yet. Start an operation from an instance to see its progress here.</Text></Paper>}
    <Stack gap="sm">{jobs.map(job => <Paper key={job.id} withBorder p="md">
      <Group justify="space-between" align="start" wrap="wrap"><Stack gap={4} style={{ minWidth: 0, flex: 1 }}><Text fw={600} style={{ overflowWrap: 'anywhere' }}>{job.kind}{job.instance_id ? ' · ' + job.instance_id : ''}</Text><Text size="sm" c="dimmed">{new Date(job.created_at).toLocaleString()}</Text><Text size="sm">{job.phase || 'Waiting to start'}</Text></Stack><Badge color={color(job)}>{job.cancel_requested && active(job) ? 'Cancelling' : job.state}</Badge><Button variant="light" aria-expanded={selected === job.id} onClick={() => setSelected(selected === job.id ? null : job.id)}>Details</Button></Group>
      {active(job) && <Progress mt="sm" value={job.progress} aria-label={job.kind + ' progress'} />}
      {selected === job.id && <JobDetails key={job.id} jobID={job.id} onUpdate={next => setJobs(current => current.map(item => item.id === next.id ? next : item))} />}
    </Paper>)}</Stack>
  </Stack>;
}
function JobDetails({ jobID, onUpdate }: { jobID: string; onUpdate: (job: Job) => void }) {
  const { user } = useAuth();
  const [job, setJob] = useState<Job | null>(null);
  const [logs, setLogs] = useState<JobLog[]>([]);
  const [error, setError] = useState('');
  const [connection, setConnection] = useState('Connecting');
  const [cancelling, setCancelling] = useState(false);
  const [notice, setNotice] = useState('');
  const lastID = useRef(0);
  const onUpdateRef = useRef(onUpdate); onUpdateRef.current = onUpdate;
  const update = useCallback((next: Job) => { setJob(next); onUpdateRef.current(next); }, []);
  useEffect(() => {
    const controller = new AbortController();
    let refreshing = false;
    const ingest = (incoming: JobLog[]) => { if (incoming.length) setLogs(current => mergeLogs(current, incoming)); };
    const refresh = async () => {
      if (refreshing) return; refreshing = true;
      try {
        const value = await api<{ job: Job }>('/jobs/' + jobID, { signal: controller.signal }); update(value.job);
        // A job has at most 10,000 durable lines. Page to catch up after a gap,
        // keeping only the newest 500 lines in browser memory.
        let cursor = lastID.current;
        for (let page = 0; page < 20; page++) {
          const value = await api<{ logs: JobLog[] }>('/jobs/' + jobID + '/logs?after=' + cursor + '&limit=500', { signal: controller.signal });
          ingest(value.logs); if (value.logs.length) { cursor = value.logs[value.logs.length - 1].id; lastID.current = cursor; } if (value.logs.length < 500) break;
        }
        setError('');
      } catch (error) { if (!controller.signal.aborted) setError(message(error)); }
      finally { refreshing = false; }
    };
    void refresh();
    const stream = new EventSource('/api/v1/jobs/' + encodeURIComponent(jobID) + '/events');
    stream.onopen = () => { setConnection('Live'); void refresh(); };
    stream.onerror = () => { setConnection('Reconnecting; progress may be delayed'); };
    stream.addEventListener('progress', event => { try { const value = JSON.parse((event as MessageEvent<string>).data) as JobEvent; if (value.job) update(value.job); } catch { setError('Could not read job progress. Refresh to reconnect.'); } });
    stream.addEventListener('log', event => { try { const value = JSON.parse((event as MessageEvent<string>).data) as JobEvent; if (value.log) ingest([value.log]); } catch { setError('Could not read job output. Download the log to inspect it.'); } });
    return () => { controller.abort(); stream.close(); };
  }, [jobID, update]);
  const canCancel = !!job && active(job) && job.cancellable && !job.cancel_requested && (user?.role === 'admin' || user?.role === 'operator' && user.id === job.requested_by);
  async function cancel() {
    setCancelling(true); setError(''); setNotice('');
    try { const value = await api<{ job: Job }>('/jobs/' + jobID + '/cancel', { method: 'POST' }); update(value.job); setNotice(value.job.state === 'cancelled' ? 'Job cancelled.' : 'Cancellation requested. Waiting for the operation to finish cleanup.'); }
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
    {job && <Text size="sm">{job.phase || 'Queued'} · {job.progress}%{job.cancel_requested && active(job) ? ' · Waiting for cancellation cleanup' : ''}</Text>}
    <Group wrap="wrap"><Button component="a" variant="light" href={'/api/v1/jobs/' + jobID + '/logs/download'}>Download log</Button>{canCancel && <Button color="red" variant="light" loading={cancelling} onClick={() => void cancel()}>Cancel job</Button>}</Group>
    <Text size="xs" c="dimmed">Showing the newest 500 lines. The download and paginated API include earlier output.</Text>
    <Code block aria-label="Job output" style={{ maxHeight: 320, overflow: 'auto', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{logs.length ? logs.map(line => new Date(line.created_at).toLocaleTimeString() + ' ' + line.message).join('\n') : 'No output yet.'}</Code>
  </Stack>;
}

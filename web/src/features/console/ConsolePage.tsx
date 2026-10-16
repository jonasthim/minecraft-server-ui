import { useEffect, useRef, useState } from 'react';
import { Alert, Anchor, Badge, Button, Code, Group, Loader, Paper, Select, SimpleGrid, Stack, Text, TextInput, Title } from '@mantine/core';
import { Link, useParams } from 'react-router-dom';
import { api } from '../../api/client';
import type { components, operations } from '../../api/schema';
import { useAuth } from '../auth/AuthProvider';

type Line = components['schemas']['ConsoleLine'];
type State = components['schemas']['ConsoleState'];
type Page = components['schemas']['ConsolePage'];
type ConsoleEvent = components['schemas']['ConsoleEvent'];
type Job = components['schemas']['Job'];
type JobLog = components['schemas']['JobLog'];
type CommandResponse = operations['consoleCommands']['responses'][202]['content']['application/json'];
const errorText = (error: unknown) => error instanceof Error ? error.message : 'Console request failed.';
const active = (job: Job) => job.state === 'queued' || job.state === 'running';
const merge = (previous: Line[], incoming: Line[]) => [...new Map([...previous, ...incoming].map(line => [line.id, line])).values()].sort((a, b) => a.id - b.id).slice(-500);

export function ConsolePage() {
  const { instanceId } = useParams();
  return <Stack gap="lg"><Anchor component={Link} to={'/instances/' + encodeURIComponent(instanceId ?? '')}>Back to server</Anchor><ConsolePanel key={instanceId} instanceId={instanceId ?? ''} /></Stack>;
}

export function ConsolePanel({ instanceId }: { instanceId: string }) {
  const { user } = useAuth();
  const [lines, setLines] = useState<Line[]>([]);
  const [state, setState] = useState<State | null>(null);
  const [connection, setConnection] = useState('Connecting to console');
  const [error, setError] = useState('');
  const [gap, setGap] = useState(false);
  const [paused, setPaused] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const cursor = useRef(0);
  const endpoint = '/instances/' + encodeURIComponent(instanceId) + '/console';

  useEffect(() => {
    const controller = new AbortController();
    let stream: EventSource | undefined;
    if (paused) return;
    const ingest = (page: Page) => {
      setLines(previous => merge(previous, page.lines));
      cursor.current = page.next_cursor;
      if (page.gap || page.lines.some(line => line.gap)) setGap(true);
    };
    const connect = async () => {
      setConnection('Connecting to console'); setError('');
      try {
        // Catch up through the bounded durable history; browser memory holds 500 lines.
        for (let page = 0; page < 4; page++) {
          const value = await api<Page>(endpoint + '/logs?after=' + cursor.current + '&limit=500', { signal: controller.signal });
          if (controller.signal.aborted) return;
          ingest(value);
          if (value.lines.length < 500) break;
        }
        try {
          const value = await api<components['schemas']['ConsoleStateResponse']>(endpoint + '/status', { signal: controller.signal });
          if (!controller.signal.aborted) setState(value.state);
        } catch (error) {
          if (!controller.signal.aborted) setError('Readiness observation unavailable. ' + errorText(error));
        }
        if (controller.signal.aborted) return;
        stream = new EventSource('/api/v1' + endpoint + '/events?after=' + cursor.current + '&limit=500');
        stream.onopen = () => setConnection('Console stream connected');
        stream.onerror = () => setConnection('Disconnected; reconnecting and checking access');
        stream.addEventListener('console', event => {
          try {
            const value = JSON.parse((event as MessageEvent<string>).data) as ConsoleEvent;
            if (!value.page || !Array.isArray(value.page.lines) || value.page.lines.length > 500 || !Number.isSafeInteger(value.page.next_cursor) || value.page.next_cursor < 0 || !value.state || typeof value.state.logs_available !== 'boolean' || value.page.lines.some(line => !Number.isSafeInteger(line.id) || typeof line.message !== 'string' || line.message.length > 8192)) throw Error('Invalid console snapshot');
            ingest(value.page); setState(value.state); setError(''); setConnection('Console stream connected');
          } catch { stream?.close(); setConnection('Console stream unavailable'); setError('Could not read console output. Reconnect to load retained history.'); }
        });
      } catch (error) { if (!controller.signal.aborted) { setError(errorText(error)); setConnection('Console stream unavailable'); } }
    };
    void connect();
    return () => { controller.abort(); stream?.close(); };
  }, [endpoint, paused, attempt]);

  return <Stack gap="md" style={{ minWidth: 0 }}>
    <Group justify="space-between" wrap="wrap"><Title order={2}>Console</Title><Group gap="xs" wrap="wrap"><Button variant="default" onClick={() => { setPaused(value => !value); if (!paused) setConnection('Console updates paused'); }}>{paused ? 'Resume updates' : 'Pause updates'}</Button><Button variant="light" onClick={() => { setPaused(false); setAttempt(value => value + 1); }}>Reconnect</Button></Group></Group>
    {error && <Alert color="red" role="alert">{error}</Alert>}
    <Paper withBorder p="md"><Stack gap="sm"><Group justify="space-between" wrap="wrap"><Title order={3} size="h4">Server readiness</Title><Badge color={state?.ready_for_use ? 'green' : 'gray'}>{state?.ready_for_use ? 'Ready for use' : state?.process_running ? 'Running; readiness unverified' : 'Not ready'}</Badge></Group>
      {!state ? <Loader size="sm" aria-label="Checking readiness" /> : <><SimpleGrid cols={{ base: 1, sm: 3 }} spacing="xs"><Text size="sm">JVM: {state.process_running ? 'Running' : 'Not running'}</Text><Text size="sm">Control: {state.control_connected ? 'Acknowledged' : 'Unverified'}</Text><Text size="sm">Required settings: {state.required_settings_verified ? 'Verified' : 'Unverified'}</Text></SimpleGrid><Text size="sm">{state.reason}</Text><Text size="xs" c="dimmed">{state.source ? state.source.replaceAll('_', ' ') + '. ' : ''}Plugin and mod gameplay overrides are unverified.</Text></>}
    </Stack></Paper>
    <Stack gap="xs"><Group justify="space-between" wrap="wrap"><Title order={3} size="h4">Server output</Title><Text size="sm" c="dimmed" role="status">{connection}</Text></Group>
      {state && !state.logs_available && <Alert color="orange" title="Live logs unavailable">{state.logs_reason}</Alert>}
      {gap && <Alert color="yellow" title="Console history has gaps">Some output was rotated, removed, or too large to display. The retained output below may be incomplete.</Alert>}
      <Text size="xs" c="dimmed">Newest 500 retained lines. Credentials are redacted. Updates reconnect regularly to check your access.</Text>
      <Code block tabIndex={0} aria-label="Server console output" style={{ height: 360, maxWidth: '100%', overflow: 'auto', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', fontSize: 12, lineHeight: 1.7 }}>{lines.length ? lines.map(line => new Date(line.created_at).toLocaleTimeString() + ' ' + line.message).join('\n') : 'No captured output yet. Output appears after the server writes to its console.'}</Code>
    </Stack>
    {user?.role === 'operator' || user?.role === 'admin' ? <CommandPanel key={instanceId + user.role} endpoint={endpoint} admin={user.role === 'admin'} /> : <Text size="sm" c="dimmed">An operator or administrator can run approved server commands.</Text>}
  </Stack>;
}

function CommandPanel({ endpoint, admin }: { endpoint: string; admin: boolean }) {
  const [verb, setVerb] = useState('list');
  const [announcement, setAnnouncement] = useState('');
  const [busy, setBusy] = useState(false);
  const [job, setJob] = useState<Job | null>(null);
  const [output, setOutput] = useState<JobLog[]>([]);
  const [error, setError] = useState('');
  const sending = busy || !!job && active(job);
  useEffect(() => {
    if (!job || !active(job)) return;
    const controller = new AbortController();
    let timer: number | undefined;
    const id = job.id;
    const poll = async () => {
      try {
        const value = await api<{ job: Job }>('/jobs/' + encodeURIComponent(id), { signal: controller.signal });
        if (controller.signal.aborted) return;
        setJob(value.job); setError('');
        if (active(value.job)) timer = window.setTimeout(() => void poll(), 1000);
        else {
          const logs = await api<{ logs: JobLog[] }>('/jobs/' + encodeURIComponent(id) + '/logs?limit=50', { signal: controller.signal });
          if (!controller.signal.aborted) setOutput(logs.logs);
        }
      } catch (error) { if (!controller.signal.aborted) { setError(errorText(error)); timer = window.setTimeout(() => void poll(), 3000); } }
    };
    void poll();
    return () => { controller.abort(); window.clearTimeout(timer); };
  }, [job?.id]);
  async function submit(event: React.FormEvent) {
    event.preventDefault(); setError(''); setOutput([]); setBusy(true);
    try {
      const input: components['schemas']['ConsoleCommandRequest'] = { command: verb === 'say' ? 'say ' + announcement : verb };
      const value = await api<CommandResponse>(endpoint + '/commands', { method: 'POST', body: JSON.stringify(input) }); setJob(value.job);
    } catch (error) { setError(errorText(error)); }
    finally { setBusy(false); }
  }
  return <Paper withBorder p="md"><form onSubmit={event => void submit(event)}><Stack gap="sm"><Title order={3} size="h4">Run approved command</Title><Text size="sm" c="dimmed">List players, announce a message{admin ? ', or read the world seed' : ''}. Other commands use dedicated server controls.</Text>
    <Select label="Command" value={verb} onChange={value => setVerb(value ?? 'list')} allowDeselect={false} data={[{ value: 'list', label: 'List online players' }, { value: 'say', label: 'Announce a message' }, ...(admin ? [{ value: 'seed', label: 'Read world seed' }] : [])]} disabled={sending} />
    {verb === 'say' && <TextInput label="Announcement" description="Plain text, up to 512 UTF-8 bytes. Player selectors are unavailable." required maxLength={512} value={announcement} onChange={event => setAnnouncement(event.currentTarget.value)} disabled={sending} />}
    {error && <Alert color="red" role="alert">{error}</Alert>}
    <Button type="submit" loading={sending} style={{ alignSelf: 'flex-start' }}>Run command</Button>
    {job && <Stack gap={4}><Text size="sm" role="status">{job.state === 'succeeded' ? 'Server acknowledged command.' : job.state === 'failed' ? 'Command failed.' : job.state === 'cancelled' ? 'Command cancelled.' : 'Command queued; waiting for server acknowledgement.'}</Text>{job.error && <Alert color="red">{job.error}</Alert>}{output.length > 0 && <Code block style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{output.map(line => line.message).join('\n')}</Code>}<Anchor component={Link} to="/jobs" size="sm">View command job</Anchor></Stack>}
  </Stack></form></Paper>;
}

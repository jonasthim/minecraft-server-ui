import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { Alert, Anchor, Badge, Button, Code, Group, Paper, Select, SimpleGrid, Stack, Switch, Text, TextInput, Title } from '@mantine/core';
import { Link, useParams } from 'react-router-dom';
import { api } from '../../api/client';
import type { components, operations } from '../../api/schema';
import { useAuth } from '../auth/AuthProvider';
import { checkedPage, mergeLines, presentation, type Line, type Page } from './console';

type State = components['schemas']['ConsoleState'];
type ConsoleEvent = components['schemas']['ConsoleEvent'];
type Job = components['schemas']['Job'];
type JobLog = components['schemas']['JobLog'];
type CommandResponse = operations['consoleCommands']['responses'][202]['content']['application/json'];
const errorText = (error: unknown) => error instanceof Error ? error.message : 'Console request failed.';
const readSignal = (signal: AbortSignal) => AbortSignal.any([signal, AbortSignal.timeout(15_000)]);
const active = (job: Job) => job.state === 'queued' || job.state === 'running';

export function ConsolePage() {
  const { instanceId } = useParams();
  return <Stack gap="lg"><Anchor component={Link} to={'/instances/' + encodeURIComponent(instanceId ?? '')}>Back to server</Anchor><ConsolePanel instanceId={instanceId ?? ''} /></Stack>;
}

export function ConsolePanel({ instanceId }: { instanceId: string }) {
  return <InstanceConsole key={instanceId} instanceId={instanceId} />;
}

function InstanceConsole({ instanceId }: { instanceId: string }) {
  const { user } = useAuth();
  const [lines, setLines] = useState<Line[]>([]);
  const [state, setState] = useState<State | null>(null);
  const [connection, setConnection] = useState('Connecting to console');
  const [error, setError] = useState('');
  const [gap, setGap] = useState(false);
  const [paused, setPaused] = useState(false);
  const [follow, setFollow] = useState(true);
  const [filter, setFilter] = useState('');
  const [attempt, setAttempt] = useState(0);
  const [history, setHistory] = useState<Page | null>(null);
  const [historyCursors, setHistoryCursors] = useState<number[]>([0]);
  const [historyBusy, setHistoryBusy] = useState(false);
  const [historyError, setHistoryError] = useState('');
  const [exportBusy, setExportBusy] = useState(false);
  const [exportStatus, setExportStatus] = useState('');
  const cursor = useRef(0);
  const viewport = useRef<HTMLDivElement>(null);
  const historyRequest = useRef<AbortController | null>(null);
  const exportRequest = useRef<AbortController | null>(null);
  const endpoint = '/instances/' + encodeURIComponent(instanceId) + '/console';
  const readinessFresh = !paused && connection === 'Console stream connected';
  const displayed = history ? history.lines : lines;
  const visible = displayed.filter(line => line.message.toLocaleLowerCase().includes(filter.toLocaleLowerCase()));

  useEffect(() => () => { historyRequest.current?.abort(); exportRequest.current?.abort(); }, []);
  useLayoutEffect(() => {
    if (follow && !paused && !history && viewport.current) viewport.current.scrollTop = viewport.current.scrollHeight;
  }, [lines, follow, filter, paused, history]);

  useLayoutEffect(() => { if (history && viewport.current) viewport.current.scrollTop = 0; }, [history]);

  useEffect(() => {
    const controller = new AbortController();
    let stream: EventSource | undefined;
    if (paused) return;
    const ingest = (input: Page) => {
      const page = checkedPage(input);
      const rewound = page.next_cursor < cursor.current;
      if (rewound && !page.gap) throw Error('Console cursor moved backwards without a gap.');
      setLines(previous => mergeLines(rewound ? [] : previous, page.lines));
      cursor.current = page.next_cursor;
      if (page.gap || page.lines.some(line => line.gap)) setGap(true);
    };
    const connect = async () => {
      setConnection('Connecting to console'); setError('');
      try {
        // Catch up retained history, then let the stream continue from this cursor.
        for (let page = 0; page < 4; page++) {
          const value = await api<Page>(endpoint + '/logs?after=' + cursor.current + '&limit=500', { signal: readSignal(controller.signal) });
          if (controller.signal.aborted) return;
          ingest(value);
          if (value.lines.length < 500) break;
        }
        try {
          const value = await api<components['schemas']['ConsoleStateResponse']>(endpoint + '/status', { signal: readSignal(controller.signal) });
          if (!controller.signal.aborted) setState(value.state);
        } catch (error) {
          if (!controller.signal.aborted) setError('Readiness observation unavailable. ' + errorText(error));
        }
        if (controller.signal.aborted) return;
        stream = new EventSource('/api/v1' + endpoint + '/events?after=' + cursor.current + '&limit=500');
        stream.onopen = () => { if (!controller.signal.aborted) setConnection('Console stream connected'); };
        stream.onerror = () => { if (!controller.signal.aborted) setConnection('Disconnected; reconnecting and checking access'); };
        stream.addEventListener('console', event => {
          if (controller.signal.aborted) return;
          try {
            const value = JSON.parse((event as MessageEvent<string>).data) as ConsoleEvent;
            if (!value.state || typeof value.state.logs_available !== 'boolean') throw Error('Invalid console state');
            ingest(value.page); setState(value.state); setError(''); setConnection('Console stream connected');
          } catch { stream?.close(); setConnection('Console stream unavailable'); setError('Could not read console output. Reconnect to load retained history.'); }
        });
      } catch (error) { if (!controller.signal.aborted) { setError(errorText(error)); setConnection('Console stream unavailable'); } }
    };
    void connect();
    return () => { controller.abort(); stream?.close(); };
  }, [endpoint, paused, attempt]);

  async function browse(cursors: number[]) {
    historyRequest.current?.abort();
    const controller = new AbortController(); historyRequest.current = controller;
    setHistoryBusy(true); setHistoryError('');
    try {
      const page = checkedPage(await api<Page>(endpoint + '/logs?after=' + cursors.at(-1) + '&limit=500', { signal: readSignal(controller.signal) }));
      if (!controller.signal.aborted) { setHistory(page); setHistoryCursors(cursors); }
    } catch (error) { if (!controller.signal.aborted) setHistoryError(errorText(error)); }
    finally { if (!controller.signal.aborted) setHistoryBusy(false); }
  }

  async function download() {
    exportRequest.current?.abort();
    const controller = new AbortController(); exportRequest.current = controller;
    const through = cursor.current;
    setExportBusy(true); setExportStatus('');
    try {
      let after = 0; let bytes = 0; let count = 0; let hasGap = gap; let limited = true;
      const output: string[] = [];
      // Independent export cursor: never changes the live or browser cursor.
      pages: for (let pageNumber = 0; pageNumber < 8; pageNumber++) {
        const page = checkedPage(await api<Page>(endpoint + '/logs?after=' + after + '&limit=500', { signal: readSignal(controller.signal) }));
        if (controller.signal.aborted) return;
        if (page.next_cursor < after) throw Error('Retained history changed during download. Retry the download.');
        hasGap ||= page.gap || page.lines.some(line => line.gap);
        for (const line of page.lines) {
          if (line.id > through) break;
          const text = line.created_at + ' #' + line.id + (line.gap ? ' [GAP] ' : ' ') + line.message + '\n';
          const size = new TextEncoder().encode(text).byteLength;
          if (bytes + size > 4 * 1024 * 1024) break pages;
          bytes += size; count++; output.push(text);
        }
        if (page.lines.length < 500 || page.next_cursor >= through) { limited = false; break; }
        if (page.next_cursor <= after) throw Error('Console history cursor did not advance.');
        after = page.next_cursor;
      }
      if (controller.signal.aborted) return;
      const scope = `Redacted retained console excerpt for instance: ${instanceId}\nNot a complete raw log. Older/rotated output is excluded; collection is not an atomic snapshot.\nUnfiltered, ascending IDs through observed cursor ${through}; ${count} lines. Limits: 8 pages / 4 MiB.\n${limited ? 'LIMIT REACHED: excerpt truncated. ' : ''}${hasGap ? 'GAPS REPORTED: output is incomplete.' : 'No API gap reported; completeness is not established.'}\n\n`;
      const url = URL.createObjectURL(new Blob([scope, ...output], { type: 'text/plain;charset=utf-8' }));
      const anchor = document.createElement('a'); anchor.href = url; anchor.download = instanceId.replace(/[^a-zA-Z0-9_-]/g, '_') + '-retained-console.txt'; anchor.click();
      window.setTimeout(() => URL.revokeObjectURL(url), 1000);
      setExportStatus(`Downloaded ${count} unfiltered retained lines through cursor ${through}.${limited ? ' Download limit reached; excerpt truncated.' : ''}${hasGap ? ' Gaps reported.' : ''}`);
    } catch (error) { if (!controller.signal.aborted) setExportStatus('Download failed. ' + errorText(error)); }
    finally { if (!controller.signal.aborted) setExportBusy(false); }
  }

  return <Stack gap="md" style={{ minWidth: 0 }}>
    <Group justify="space-between" wrap="wrap"><Title order={2}>Console</Title><Group gap="xs" wrap="wrap"><Button variant="default" onClick={() => { setPaused(value => !value); if (!paused) setConnection('Console updates paused'); }}>{paused ? 'Resume updates' : 'Pause updates'}</Button><Button variant="light" onClick={() => { setPaused(false); setAttempt(value => value + 1); }}>Reconnect</Button></Group></Group>
    {error && <Alert color="red" role="alert">{error}</Alert>}
    <Paper withBorder p="md"><Stack gap="sm"><Group justify="space-between" wrap="wrap"><Title order={3} size="h4">Server readiness</Title><Badge color={readinessFresh && state?.ready_for_use ? 'green' : 'gray'}>{state && !readinessFresh ? 'Readiness unverified' : state?.ready_for_use ? 'Ready for use' : state?.process_running ? 'Running; readiness unverified' : 'Not ready'}</Badge></Group>
      {state && !readinessFresh && <Text size="sm" c="orange" role="status">Previous readiness observation is stale. Live verification is unavailable while console updates are paused, connecting, or disconnected.</Text>}
      {!state ? <Text size="sm">Readiness observation pending or unavailable.</Text> : <><SimpleGrid cols={{ base: 1, sm: 3 }} spacing="xs"><Text size="sm">JVM: {state.process_running ? 'Running' : 'Not running'}</Text><Text size="sm">Control: {state.control_connected ? 'Acknowledged' : 'Unverified'}</Text><Text size="sm">Required settings: {state.required_settings_verified ? 'Verified' : 'Unverified'}</Text></SimpleGrid><Text size="sm">{state.reason}</Text><Text size="xs" c="dimmed">{state.source ? state.source.replaceAll('_', ' ') + '. ' : ''}Plugin and mod gameplay overrides are unverified.</Text></>}
    </Stack></Paper>
    <Stack gap="xs"><Group justify="space-between" wrap="wrap"><Title order={3} size="h4">Server output</Title><Text size="sm" c="dimmed" role="status">{connection}</Text></Group>
      {state && !state.logs_available && <Alert color="orange" title="Live logs unavailable">{state.logs_reason}</Alert>}
      {(gap || history?.gap || history?.lines.some(line => line.gap)) && <Alert color="yellow" title="Console history has gaps">Some output was rotated, removed, or too large to display. Retained output may be incomplete.</Alert>}
      <Text size="xs" c="dimmed">Live view: up to 500 buffered lines. The manager retains up to 2,000 redacted lines; older raw logs are outside this browser. No complete-log guarantee.</Text>
      <TextInput label="Filter console text" description="Case insensitive text in the current buffer or retained page. Downloads include unfiltered output." value={filter} onChange={event => setFilter(event.currentTarget.value)} maxLength={512} />
      <Group wrap="wrap"><Switch label="Follow live output" checked={follow} disabled={!!history} onChange={event => setFollow(event.currentTarget.checked)} /><Button variant="default" loading={historyBusy} onClick={() => void browse([0])}>Browse retained history</Button>{history && <Button variant="light" onClick={() => { historyRequest.current?.abort(); setHistoryBusy(false); setHistory(null); setHistoryError(''); }}>Return to live output</Button>}</Group>
      {historyError && <Alert color="red" role="alert">{historyError}</Alert>}
      {history ? <Group wrap="wrap"><Text size="sm">Retained page {historyCursors.length} · {history.lines.length} lines · cursor {history.next_cursor}</Text><Button size="xs" variant="default" disabled={historyBusy || historyCursors.length <= 1} onClick={() => void browse(historyCursors.slice(0, -1))}>Previous page</Button><Button size="xs" variant="default" disabled={historyBusy || history.lines.length < 500 || history.next_cursor <= (historyCursors.at(-1) ?? 0) || historyCursors.length >= 64} onClick={() => void browse([...historyCursors, history.next_cursor])}>Next page</Button><Text size="xs" c="dimmed">Pages read current retention; concurrent rotation can create gaps. Start again to refresh. Browsing is limited to 64 pages.</Text></Group> : <Text size="sm">{lines.length} buffered lines · cursor {cursor.current}</Text>}
      <Text size="xs" c="dimmed">{visible.length} of {displayed.length} lines shown. {history ? 'Retained history (oldest first).' : paused ? 'Updates paused; follow is suspended.' : follow ? 'Following live output.' : 'Follow off; new output continues collecting.'}</Text>
      <div ref={viewport} tabIndex={0} role="region" aria-label="Server console output" onScroll={event => { const node = event.currentTarget; if (!history && node.scrollHeight - node.scrollTop - node.clientHeight > 40) setFollow(false); }} style={{ height: 360, maxWidth: '100%', overflow: 'auto', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', fontSize: 12, lineHeight: 1.7, fontFamily: 'monospace', background: 'var(--mantine-color-default-hover)', padding: 8, borderRadius: 4 }}>
        {visible.length ? visible.map(line => { const tone = presentation(line); return <div key={line.id} data-console-line={line.id} style={{ borderInlineStart: `3px solid ${tone.color}`, paddingInlineStart: 6 }}><time dateTime={line.created_at} title={line.created_at}>{new Date(line.created_at).toLocaleTimeString()}</time> <span style={{ fontWeight: 600 }}>{tone.label}</span> {line.message}</div>; }) : displayed.length ? 'No lines match this filter.' : 'No captured output yet. Output appears after the server writes to its console.'}
      </div>
      <Button variant="default" loading={exportBusy} disabled={cursor.current === 0} onClick={() => void download()} style={{ alignSelf: 'flex-start' }}>Download retained output</Button>
      <Text size="xs" c="dimmed">Download: unfiltered redacted retention through the observed live cursor; maximum 8 pages / 4 MiB. Includes scope and gap notes. New output and older raw files are excluded.</Text>
      {exportStatus && <Text size="sm" role="status">{exportStatus}</Text>}
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

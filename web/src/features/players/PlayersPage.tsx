import { useEffect, useState } from 'react';
import { Alert, Anchor, Badge, Button, Group, Paper, Select, SimpleGrid, Stack, Text, TextInput, Title } from '@mantine/core';
import { Link, useParams } from 'react-router-dom';
import { api } from '../../api/client';
import type { components } from '../../api/schema';
import { useAuth } from '../auth/AuthProvider';

type Snapshot = components['schemas']['PlayerSnapshot'];
type List = components['schemas']['PlayerList'];
type History = components['schemas']['PlayerHistoryPage'];
type Mutation = components['schemas']['PlayerMutationRequest'];
type Identity = components['schemas']['PlayerIdentity'];
type Job = components['schemas']['Job'];
const errorText = (error: unknown) => error instanceof Error ? error.message : 'Player request failed.';
const when = (value: string) => value.startsWith('0001-') ? 'Not observed' : new Date(value).toLocaleString();
const label = (player: Identity) => player.name || player.uuid || 'Unknown identity';
const actionOptions: { value: Mutation['action']; label: string }[] = [
  { value: 'kick', label: 'Kick player' }, { value: 'whitelist_add', label: 'Add to whitelist' }, { value: 'whitelist_remove', label: 'Remove from whitelist' }, { value: 'ban', label: 'Ban player' }, { value: 'unban', label: 'Unban player' }, { value: 'op', label: 'Grant operator privileges' }, { value: 'deop', label: 'Remove operator privileges' },
];
export function PlayersPage() {
  const { instanceId = '' } = useParams();
  return <Stack gap="lg"><Anchor component={Link} to={'/instances/' + encodeURIComponent(instanceId)}>Back to server</Anchor><PlayersPanel key={instanceId} instanceId={instanceId} /></Stack>;
}
export function PlayersPanel({ instanceId }: { instanceId: string }) {
  const { user } = useAuth();
  const endpoint = '/instances/' + encodeURIComponent(instanceId) + '/players';
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
  const [history, setHistory] = useState<History | null>(null);
  const [list, setList] = useState<List | null>(null);
  const [kind, setKind] = useState('whitelist');
  const [action, setAction] = useState<Mutation['action']>('kick');
  const [mode, setMode] = useState<Mutation['mode']>('online');
  const [name, setName] = useState('');
  const [uuid, setUUID] = useState('');
  const [level, setLevel] = useState('default');
  const [error, setError] = useState('');
  const [listError, setListError] = useState('');
  const [historyError, setHistoryError] = useState('');
  const [notice, setNotice] = useState('');
  const [attempt, setAttempt] = useState(0);
  const [busy, setBusy] = useState(false);
  const [job, setJob] = useState<Job | null>(null);
  const operating = user?.role === 'admin' || user?.role === 'operator';
  const activeJob = job?.state === 'queued' || job?.state === 'running';
  useEffect(() => {
    const controller = new AbortController();
    const load = async () => {
      try { const value = await api<components['schemas']['PlayerSnapshotResponse']>(endpoint, { signal: controller.signal }); if (!controller.signal.aborted) { setSnapshot(value.snapshot); setError(''); } } catch (error) { if (!controller.signal.aborted) setError(errorText(error)); }
    };
    void load(); const timer = window.setInterval(() => { void load(); }, 30000);
    return () => { controller.abort(); window.clearInterval(timer); };
  }, [endpoint, attempt]);
  useEffect(() => {
    const controller = new AbortController();
    setList(null); setListError('');
    void api<components['schemas']['PlayerListResponse']>(endpoint + '/lists/' + kind, { signal: controller.signal }).then(value => { if (!controller.signal.aborted) setList(value.list); }).catch(error => { if (!controller.signal.aborted) setListError(errorText(error)); });
    return () => controller.abort();
  }, [endpoint, kind, attempt]);
  useEffect(() => {
    const controller = new AbortController(); setHistoryError('');
    void api<History>(endpoint + '/history?limit=100', { signal: controller.signal }).then(value => { if (!controller.signal.aborted) setHistory(value); }).catch(error => { if (!controller.signal.aborted) setHistoryError(errorText(error)); });
    return () => controller.abort();
  }, [endpoint, attempt]);
  useEffect(() => {
    if (!job || !activeJob) return;
    const controller = new AbortController();
    const timer = window.setInterval(() => {
      void api<{ job: Job }>('/jobs/' + job.id, { signal: controller.signal }).then(value => {
        if (controller.signal.aborted) return;
        setJob(value.job);
        if (value.job.state === 'succeeded') { setNotice('Player operation acknowledged and read back.'); setAttempt(value => value + 1); }
        if (value.job.state === 'failed' || value.job.state === 'cancelled') { setNotice(''); setError(value.job.error || 'Player operation did not complete. Inspect job output before retrying.'); }
      }).catch(error => { if (!controller.signal.aborted) setError('Could not check operation result. ' + errorText(error)); });
    }, 1000);
    return () => { controller.abort(); window.clearInterval(timer); };
  }, [job, activeJob]);
  const submit = async (event: React.FormEvent) => {
    event.preventDefault(); setBusy(true); setError(''); setNotice('');
    try {
      const input: Mutation = { action, mode, ...(name ? { name } : {}), ...(uuid ? { uuid } : {}), ...(action === 'op' && level !== 'default' ? { permission_level: Number(level) } : {}) };
      const value = await api<components['schemas']['PlayerMutationResponse']>(endpoint + '/mutations', { method: 'POST', body: JSON.stringify(input) });
      setJob(value.job); setNotice('Player operation queued. Waiting for acknowledgement and readback.');
    } catch (error) { setError(errorText(error)); } finally { setBusy(false); }
  };
  const moreHistory = async () => {
    if (!history) return;
    try {
      const page = await api<History>(endpoint + '/history?limit=100&after=' + history.next_cursor + '&event_after=' + history.next_event_cursor);
      setHistory(previous => previous ? { ...page, gap: page.gap || previous.gap, event_gap: page.event_gap || previous.event_gap, sessions: [...new Map([...previous.sessions, ...page.sessions].map(item => [item.id, item])).values()].slice(-500), events: [...new Map([...previous.events, ...page.events].map(item => [item.id, item])).values()].slice(-500) } : page);
      if (!page.sessions.length && !page.events.length) setHistoryError('No more retained history. Refresh to see changes to active sessions.');
    } catch (error) { setHistoryError(errorText(error)); }
  };
  return <Stack gap="lg">
    <Group justify="space-between" wrap="wrap"><div><Title order={1}>Players</Title><Text c="dimmed">Presence, native lists and observed history</Text></div><Button variant="light" onClick={() => setAttempt(value => value + 1)}>Refresh observations</Button></Group>
    {error && <Alert color="red" role="alert">{error}</Alert>}{notice && <Alert color={activeJob ? 'blue' : 'green'} role="status">{notice}</Alert>}
    {job && <Text size="sm">Operation: {job.state} · <Anchor component={Link} to="/jobs">Open jobs and output</Anchor></Text>}
    <Paper withBorder p="lg"><Stack><Group justify="space-between"><Title order={2}>Online now</Title><Badge color={snapshot?.known ? 'forest' : 'gray'}>{snapshot?.known ? snapshot.count + ' observed' : 'Unknown'}</Badge></Group><Text size="sm">{snapshot?.reason || 'Loading player observation…'}</Text>{snapshot?.gap && <Alert color="yellow">Observation gap. The last roster does not prove who is online now.</Alert>}<Text size="sm" c="dimmed">Sampled every 30 seconds · {snapshot ? when(snapshot.observed_at) : 'Waiting for observation'}</Text>{snapshot?.players.map((player, index) => <IdentityRow key={player.uuid || player.name || index} player={player} />)}{snapshot?.known && !snapshot.players.length && <Text>No players were present at the last observation.</Text>}</Stack></Paper>
    <Paper withBorder p="lg"><Stack><Title order={2}>Native lists</Title><Select label="Player list" value={kind} onChange={value => { if (value) setKind(value); }} data={[{ value: 'whitelist', label: 'Whitelist' }, { value: 'bans', label: 'Banned players' }, { value: 'operators', label: 'Operators' }]} />{listError && <Alert color="red" role="alert">{listError}</Alert>}{list && <><Text size="sm">{list.runtime_verified ? 'Fresh native management readback' : 'Saved native JSON snapshot; runtime and plugin overrides are unverified'} · {when(list.observed_at)}</Text>{list.entries.length ? list.entries.map((entry, index) => <IdentityRow key={entry.player.uuid || index} player={entry.player} detail={entry.permission_level === undefined ? undefined : 'Permission level ' + entry.permission_level} />) : <Text>No entries in this native list.</Text>}</>}</Stack></Paper>
    {operating && <Paper withBorder p="lg"><form onSubmit={event => { void submit(event); }}><Stack><Title order={2}>Manage a player</Title><Text size="sm">Online changes need a running native server. Offline changes require a stopped server and an explicit UUID. Modern management uses UUIDs; legacy console uses an exact username and its default operator level. Ban and kick use the server's default message.</Text><SimpleGrid cols={{ base: 1, sm: 2 }}><Select label="Operation" required value={action} onChange={value => { if (value) setAction(value as Mutation['action']); }} data={actionOptions.filter(option => user?.role === 'admin' || (option.value !== 'op' && option.value !== 'deop'))} /><Select label="Server mode" required value={mode} onChange={value => { if (value) setMode(value as Mutation['mode']); }} data={[{ value: 'online', label: 'Online native operation' }, { value: 'offline', label: 'Stopped native list edit' }]} /><TextInput label="Exact username" maxLength={16} pattern="[A-Za-z0-9_]+" value={name} onChange={event => setName(event.currentTarget.value)} /><TextInput label="Instance-local player UUID" description="Use an observed or verified UUID; names never generate UUIDs." maxLength={36} pattern="[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}" value={uuid} onChange={event => setUUID(event.currentTarget.value)} /></SimpleGrid>{action === 'op' && <Select label="Operator permission level" value={level} onChange={value => { if (value) setLevel(value); }} data={[{ value: 'default', label: 'Native server default (offline: 4)' }, ...[1, 2, 3, 4].map(value => ({ value: String(value), label: String(value) }))]} />}<Button type="submit" loading={busy} disabled={activeJob}>Queue player operation</Button></Stack></form></Paper>}
    <Paper withBorder p="lg"><Stack><Title order={2}>Observed history</Title><Text size="sm">{history?.reason || 'Loading retained history…'}</Text>{historyError && <Alert color="yellow" role="status">{historyError}</Alert>}{(history?.gap || history?.event_gap) && <Alert color="yellow">History has gaps or retained records have expired.</Alert>}{history?.sessions.map(session => <Stack key={session.id} gap={2}><Text fw={600}>{label(session.player)}</Text><Text size="sm">First seen {when(session.first_seen)} · Last seen {when(session.last_seen)}</Text><Text size="sm" c="dimmed">{session.ended_at ? 'Observed boundary: ' + when(session.ended_at) + ' · ' + session.end_reason : 'Present at last sample'}{session.gap ? ' · Incomplete observation interval' : ''}</Text></Stack>)}{history?.events.map(event => <Text key={event.id} size="sm">{label(event.player)} · {event.kind.replaceAll('_', ' ')} · observed {when(event.observed_at)} · {event.player.authority}{event.gap ? ' · source gap' : ''}</Text>)}{history && !history.sessions.length && !history.events.length && <Text>No player sessions or join events have been observed yet.</Text>}<Button variant="light" disabled={!history} onClick={() => { void moreHistory(); }}>Load more retained history</Button></Stack></Paper>
  </Stack>;
}
function IdentityRow({ player, detail }: { player: Identity; detail?: string }) {
  return <Stack gap={2} style={{ minWidth: 0 }}><Text fw={600} style={{ overflowWrap: 'anywhere' }}>{label(player)}</Text><Text size="xs" c="dimmed" style={{ overflowWrap: 'anywhere' }}>{player.uuid || 'UUID unknown'} · {player.authority} · {player.source}{detail ? ' · ' + detail : ''}</Text></Stack>;
}

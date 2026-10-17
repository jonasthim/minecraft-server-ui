import { jobHref } from '../jobs';
import { useCallback, useEffect, useState } from 'react';
import { Alert, Anchor, Badge, Button, Checkbox, Group, Loader, Modal, NumberInput, Paper, Select, Stack, Text, TextInput, Title } from '@mantine/core';
import { Link, useParams } from 'react-router-dom';
import { api } from '../../api/client';
import type { components } from '../../api/schema';
import { useAuth } from '../auth/AuthProvider';

type Schedule = components['schemas']['ScheduleRecord'];
type Input = components['schemas']['ScheduleInput'];
type List = components['schemas']['ScheduleListResponse'];
type Recovery = components['schemas']['RecoveryStatus'];
type Run = components['schemas']['ScheduleRun'];
type Action = Input['action'];
const labels: Record<Action, string> = { backup: 'Automatic backup', command: 'Approved command', announcement: 'Announcement', restart: 'Restart with countdown', update: 'Pinned automatic update' };
const errorText = (error: unknown) => error instanceof Error ? error.message : 'Schedule operation failed.';
const when = (value?: string) => value ? new Date(value).toLocaleString() : 'None';
const inputOf = (s: Schedule): Input => ({ name: s.name, enabled: s.enabled, expression: s.expression, timezone: s.timezone, action: s.action, ...(s.action === 'backup' ? { backup_kind: s.backup_kind } : {}), ...(s.action === 'command' ? { command: s.command } : {}), ...(s.action === 'announcement' ? { announcement: s.announcement } : {}), ...(s.action === 'restart' ? { countdown_seconds: s.countdown_seconds } : {}) });

export function SchedulesPage() {
  const { instanceId = '' } = useParams();
  return <Stack><Anchor component={Link} to={'/instances/' + encodeURIComponent(instanceId)}>Back to server</Anchor><SchedulesPanel key={instanceId} instanceId={instanceId} /></Stack>;
}
export function SchedulesPanel({ instanceId }: { instanceId: string }) {
  const { user } = useAuth();
  const canOperate = user?.role === 'operator' || user?.role === 'admin';
  const endpoint = '/instances/' + encodeURIComponent(instanceId);
  const [data, setData] = useState<List | null>(null);
  const [recovery, setRecovery] = useState<Recovery | null>(null);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState<Schedule | 'new' | null>(null);
  const [removing, setRemoving] = useState<Schedule | null>(null);
  const [history, setHistory] = useState<{ name: string; runs: Run[] } | null>(null);
  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const [schedules, status] = await Promise.all([api<List>(endpoint + '/schedules', { signal }), api<Recovery>(endpoint + '/recovery', { signal })]);
      if (!signal?.aborted) { setData(schedules); setRecovery(status); }
    } catch (error) { if (!signal?.aborted) setError(errorText(error)); }
  }, [endpoint]);
  useEffect(() => { const controller = new AbortController(); void load(controller.signal); const timer = window.setInterval(() => { void load(controller.signal); }, 10000); return () => { controller.abort(); window.clearInterval(timer); }; }, [load]);
  const save = async (input: Input, id?: string) => {
    await api<Schedule>(endpoint + '/schedules' + (id ? '/' + encodeURIComponent(id) : ''), { method: id ? 'PUT' : 'POST', body: JSON.stringify(input) });
    setEditing(null); setNotice(input.enabled ? 'Schedule enabled. Future runs use your current account authority.' : 'Schedule saved disabled.'); await load();
  };
  const mutate = async (fn: () => Promise<void>) => { setBusy(true); setError(''); setNotice(''); try { await fn(); } catch (error) { setError(errorText(error)); } finally { setBusy(false); } };
  const showHistory = async (schedule: Schedule) => {
    await mutate(async () => { const response = await api<components['schemas']['ScheduleHistoryResponse']>(endpoint + '/schedules/' + encodeURIComponent(schedule.id) + '/runs'); setHistory({ name: schedule.name, runs: response.runs }); });
  };
  return <Stack style={{ minWidth: 0 }}>
    <Group justify="space-between" wrap="wrap"><Title order={2}>Schedules</Title><Group><Button variant="default" onClick={() => { setError(''); void load(); }}>Refresh</Button>{canOperate && <Button onClick={() => setEditing('new')}>New schedule</Button>}</Group></Group>
    {error && <Alert role="alert" color="red">{error}</Alert>}{notice && <Alert role="status" color="green">{notice}</Alert>}
    {!data && !error && <Loader aria-label="Loading schedules" />}
    {data && <><Text size="sm" c="dimmed">{data.time_policy}</Text><Text size="sm">Host timezone: {data.default_timezone || 'Choose an explicit IANA timezone when creating a schedule.'} · Restart countdown defaults to five minutes.</Text>{data.schedules.length === 0 && <Paper withBorder p="md"><Text>No schedules yet. Create a disabled definition, review its timezone and action, then enable it.</Text></Paper>}</>}
    {data?.schedules.map(schedule => {
      const owner = canOperate && (user?.role === 'admin' || user?.id === schedule.actor_id);
      return <Paper key={schedule.id} withBorder p="md" style={{ minWidth: 0, overflowWrap: 'anywhere' }}><Stack gap="xs">
        <Group justify="space-between" wrap="wrap"><Title order={3}>{schedule.name}</Title><Badge color={schedule.enabled ? 'green' : 'gray'}>{schedule.enabled ? 'Enabled' : 'Disabled'}</Badge></Group>
        <Text>{labels[schedule.action]} · {schedule.expression} · {schedule.timezone}</Text>
        <Text size="sm">Next: {when(schedule.next_run_at)}</Text>
        {schedule.last_run && <Text size="sm">Last: {when(schedule.last_run.due_at)} · {schedule.last_run.state.replaceAll('_', ' ')}{schedule.last_run.job_state ? ' · ' + schedule.last_run.job_state : ''}</Text>}
        {schedule.disabled_reason && <Alert color="yellow">{schedule.disabled_reason}</Alert>}
        <Group wrap="wrap"><Button size="xs" variant="default" loading={busy} onClick={() => { void showHistory(schedule); }}>Run history</Button>{owner && <><Button size="xs" variant="light" onClick={() => setEditing(schedule)}>Edit</Button><Button size="xs" variant="default" loading={busy} disabled={!schedule.enabled && !data.available_actions.includes(schedule.action)} onClick={() => { void mutate(() => save({ ...inputOf(schedule), enabled: !schedule.enabled }, schedule.id)); }}>{schedule.enabled ? 'Disable' : 'Enable'}</Button><Button size="xs" variant="subtle" color="red" onClick={() => setRemoving(schedule)}>Delete</Button></>}</Group>
      </Stack></Paper>;
    })}
    <Paper withBorder p="md"><Stack gap="sm"><Group justify="space-between" wrap="wrap"><Title order={3}>Crash recovery</Title><Badge color={recovery?.enabled ? 'green' : 'gray'}>{recovery?.enabled ? 'Enabled' : 'Disabled'}</Badge></Group>
      <Text size="sm">Only a proven unexpected process exit qualifies. Up to three restart attempts in any ten minutes use increasing delays of 10, 30 and 60 seconds. A deliberate stop cancels pending recovery. Readiness failures while the JVM runs do not trigger another restart.</Text>
      {recovery?.reason && <Alert color="yellow">{recovery.reason}</Alert>}
      {user?.role === 'admin' && recovery && <Button variant="default" loading={busy} onClick={() => { void mutate(async () => { await api<Recovery>(endpoint + '/recovery', { method: 'PUT', body: JSON.stringify({ enabled: !recovery.enabled }) }); setNotice(recovery.enabled ? 'Automatic crash recovery disabled.' : 'Automatic crash recovery enabled under your current administrator authority.'); await load(); }); }}>{recovery.enabled ? 'Disable automatic recovery' : 'Enable automatic recovery'}</Button>}
      {recovery?.attempts.slice(0, 5).map(attempt => <Text key={attempt.id} size="sm">{when(attempt.due_at)} · {attempt.state} {attempt.reason}{attempt.job_id && <> · <Anchor component={Link} to={jobHref(attempt.job_id)}>View job</Anchor></>}</Text>)}
    </Stack></Paper>
    <Modal closeButtonProps={{ 'aria-label': 'Close schedule editor' }} opened={editing !== null} onClose={() => setEditing(null)} title={editing === 'new' ? 'Create disabled schedule' : 'Edit schedule'} centered>{editing && data && <ScheduleForm key={editing === 'new' ? 'new' : editing.id} initial={editing === 'new' ? null : editing} timezone={data.default_timezone} available={data.available_actions} onSave={save} />}</Modal>
    <Modal closeButtonProps={{ 'aria-label': 'Close delete confirmation' }} opened={removing !== null} onClose={() => setRemoving(null)} title="Delete schedule" centered><Stack><Text>Delete {removing?.name}? Future triggers will stop. Any already admitted job remains in job history.</Text><Button color="red" loading={busy} onClick={() => { if (removing) void mutate(async () => { await api(endpoint + '/schedules/' + encodeURIComponent(removing.id), { method: 'DELETE' }); setRemoving(null); setNotice('Schedule deleted.'); await load(); }); }}>Delete schedule</Button></Stack></Modal>
    <Modal closeButtonProps={{ 'aria-label': 'Close run history' }} opened={history !== null} onClose={() => setHistory(null)} title={'Run history · ' + (history?.name || '')} centered><Stack>{history?.runs.length === 0 && <Text>No occurrences yet.</Text>}{history?.runs.map(run => <Paper key={run.id} withBorder p="sm"><Text>{when(run.due_at)}</Text><Text size="sm">{run.state.replaceAll('_', ' ')}{run.job_state ? ' · ' + run.job_state : ''}</Text>{run.reason && <Text size="sm">{run.reason}</Text>}{run.job_id && <Anchor component={Link} to={jobHref(run.job_id)}>View jobs and output</Anchor>}</Paper>)}</Stack></Modal>
  </Stack>;
}
function ScheduleForm({ initial, timezone, available, onSave }: { initial: Schedule | null; timezone: string; available: Action[]; onSave: (input: Input, id?: string) => Promise<void> }) {
  const [name, setName] = useState(initial?.name || '');
  const [action, setAction] = useState<Action>(initial?.action || 'backup');
  const [expression, setExpression] = useState(initial?.expression || '0 3 * * *');
  const [zone, setZone] = useState(initial?.timezone || timezone);
  const [enabled, setEnabled] = useState(initial?.enabled || false);
  const [command, setCommand] = useState(initial?.command || 'list');
  const [announcement, setAnnouncement] = useState(initial?.announcement || '');
  const [kind, setKind] = useState<'world' | 'full'>(initial?.backup_kind || 'world');
  const [countdown, setCountdown] = useState(initial?.countdown_seconds ?? 300);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const submit = async (event: React.FormEvent) => {
    event.preventDefault(); setBusy(true); setError('');
    const input: Input = { name, enabled: !!initial && enabled && available.includes(action), action, expression, timezone: zone, ...(action === 'backup' ? { backup_kind: kind } : {}), ...(action === 'command' ? { command } : {}), ...(action === 'announcement' ? { announcement } : {}), ...(action === 'restart' ? { countdown_seconds: countdown } : {}) };
    try { await onSave(input, initial?.id); } catch (error) { setError(errorText(error)); } finally { setBusy(false); }
  };
  return <form onSubmit={submit}><Stack>{error && <Alert color="red" role="alert">{error}</Alert>}<TextInput label="Name" required maxLength={80} value={name} onChange={e => setName(e.currentTarget.value)} /><Select label="Action" required value={action} data={Object.entries(labels).map(([value, label]) => ({ value, label }))} onChange={value => { if (value && value in labels) setAction(value as Action); }} /><TextInput label="Cron or interval" description="Five fields: minute hour day month weekday. Descriptors such as @daily and @every 6h are accepted. Minimum interval is one minute." required maxLength={256} value={expression} onChange={e => setExpression(e.currentTarget.value)} /><TextInput label="IANA timezone" description="Stored independently of future host timezone changes, for example Europe/Stockholm or UTC." required maxLength={128} value={zone} onChange={e => setZone(e.currentTarget.value)} />
    {action === 'backup' && <Select label="Backup scope" value={kind} data={[{ value: 'world', label: 'World and dimensions' }, { value: 'full', label: 'Full local server and addon data' }]} onChange={value => { if (value === 'world' || value === 'full') setKind(value); }} />}
    {action === 'command' && <TextInput label="Approved command" description="list, say plain text, or administrator-only seed. Other commands use dedicated instance controls." required maxLength={516} value={command} onChange={e => setCommand(e.currentTarget.value)} />}
    {action === 'announcement' && <TextInput label="Announcement" description="Plain text without selectors; delivery requires server acknowledgement." required maxLength={512} value={announcement} onChange={e => setAnnouncement(e.currentTarget.value)} />}
    {action === 'restart' && <NumberInput label="Countdown in seconds" description="Five-minute default. Fresh player observations and acknowledged announcements are required." required min={0} max={3600} allowDecimal={false} value={countdown} onChange={value => setCountdown(Number(value))} />}
    {action === 'update' && <Alert color="yellow">Updates keep the selected Minecraft version pinned. Compatibility, unresolved conflicts and a successful safety backup must be checked by the update service.</Alert>}
    {!available.includes(action) && <Alert color="yellow">This action is currently unavailable. The definition can be saved disabled.</Alert>}
    {initial && <Checkbox label="Enable future scheduled runs" checked={enabled} disabled={!available.includes(action)} onChange={e => setEnabled(e.currentTarget.checked)} />}
    <Button type="submit" loading={busy}>{initial ? 'Save schedule' : 'Save disabled schedule'}</Button></Stack></form>;
}

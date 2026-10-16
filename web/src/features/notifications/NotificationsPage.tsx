import { useEffect, useState } from 'react';
import { Alert, Badge, Button, Checkbox, Group, NumberInput, Paper, PasswordInput, Select, SimpleGrid, Stack, Text, TextInput, Title } from '@mantine/core';
import { api } from '../../api/client';
import type { components } from '../../api/schema';
import { useAuth } from '../auth/AuthProvider';

type Channel = components['schemas']['NotificationChannel'];
type Input = components['schemas']['NotificationChannelInput'];
type Destination = components['schemas']['NotificationDestination'];
type History = components['schemas']['NotificationHistory'];
type Provider = components['schemas']['NotificationProvider'];
const providers: Provider[] = ['discord', 'slack', 'ntfy', 'telegram', 'webhook', 'email'];
const empty = (): Destination => ({ endpoint: '', secret: '', recipient: '', smtp_host: '', smtp_port: 587, smtp_user: '', smtp_password: '', from: '', tls_mode: 'starttls' });
const message = (error: unknown) => error instanceof Error ? error.message : 'Notification request failed.';
export function NotificationsPage() {
  const { user } = useAuth();
  const [channels, setChannels] = useState<Channel[]>([]);
  const [history, setHistory] = useState<History | null>(null);
  const [error, setError] = useState('');
  const [success, setSuccess] = useState('');
  const [selected, setSelected] = useState<Channel | null>(null);
  const [version, setVersion] = useState(0);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (user?.role !== 'admin') return;
    const controller = new AbortController();
    void Promise.all([api<components['schemas']['NotificationChannels']>('/notifications/channels', { signal: controller.signal }), api<History>('/notifications/history?limit=50', { signal: controller.signal })]).then(([list, page]) => { if (!controller.signal.aborted) { setChannels(list.channels); setHistory(page); setError(''); } }).catch(error => { if (!controller.signal.aborted) setError(message(error)); });
    return () => controller.abort();
  }, [user?.role, version]);
  if (user?.role !== 'admin') return <Alert color="orange">Administrator access is required to configure destinations or view notification history.</Alert>;
  async function remove(channel: Channel) {
    setBusy(true); setError(''); setSuccess('');
    try { await api('/notifications/channels/' + encodeURIComponent(channel.id), { method: 'DELETE' }); setSuccess('Destination deleted; queued deliveries canceled.'); setSelected(null); setVersion(v => v + 1); }
    catch (error) { setError(message(error)); } finally { setBusy(false); }
  }
  async function older() {
    if (!history?.next_cursor) return;
    setBusy(true); setError('');
    try { const next = await api<History>('/notifications/history?' + new URLSearchParams({ before: String(history.next_cursor), limit: '50' })); setHistory(previous => previous ? { ...next, deliveries: [...previous.deliveries, ...next.deliveries].slice(-200) } : next); }
    catch (error) { setError(message(error)); } finally { setBusy(false); }
  }
  return <Stack style={{ minWidth: 0, overflowWrap: 'anywhere' }}>
    <Group justify="space-between"><Title order={1}>Notifications</Title><Button variant="light" onClick={() => setVersion(v => v + 1)}>Refresh notifications</Button></Group>
    <Text>Choose Discord, Slack, ntfy, Telegram, a generic webhook or email. New destinations are disabled; save first, then explicitly enable delivery. Events come from completed backup, restore, lifecycle and content jobs. Success notifications are optional.</Text>
    <Text size="sm">Destination URLs, tokens, SMTP credentials and recipient details stay private. Replace supplies the complete destination; clear erases it and disables delivery. Changes cancel queued messages. No test message is sent by saving.</Text>
    {error && <Alert role="alert" color="red">{error}</Alert>}{success && <Alert role="status" color="green">{success}</Alert>}
    <SimpleGrid cols={{ base: 1, md: 2 }}>{channels.map(channel => <Paper key={channel.id} withBorder p="md"><Stack gap="xs"><Group justify="space-between"><Text fw={600}>{channel.label}</Text><Badge color={channel.enabled ? 'green' : 'gray'}>{channel.enabled ? 'Enabled' : 'Disabled'}</Badge></Group><Text size="sm">{channel.provider} · {channel.configured ? 'Destination stored (masked)' : 'Destination missing'} · {channel.events.join(', ') || 'No events selected'}</Text><Text size="xs">Credentials: {channel.secret_set ? 'stored (masked)' : 'none'} · Recipient: {channel.recipient_set ? 'stored (masked)' : 'none'}</Text><Group><Button variant="light" onClick={() => setSelected(channel)}>Edit {channel.label}</Button><Button color="red" variant="subtle" loading={busy} onClick={() => void remove(channel)}>Delete {channel.label}</Button></Group></Stack></Paper>)}</SimpleGrid>
    {channels.length === 0 && <Text>No notification destinations configured. Delivery is disabled by default.</Text>}
    <Group><Button variant="default" onClick={() => setSelected(null)}>New destination</Button></Group>
    <ChannelForm key={(selected?.id || 'new') + ':' + version} channel={selected} onSaved={() => { setSelected(null); setSuccess('Notification configuration saved.'); setVersion(v => v + 1); }} />
    <Title order={2}>Delivery history</Title>
    <Text size="sm">At most three attempts, 30 seconds then 2 minutes between retries, one global delivery at a time and at least one second between attempts. Transport failures can be ambiguous and retries can duplicate delivery. In-flight delivery after a crash is marked unknown and is never replayed automatically. History is retained for 30 days, up to 10,000 entries.</Text>
    {history?.deliveries.length === 0 && <Text>No delivery history yet. Select events and explicitly enable a destination to receive future job outcomes.</Text>}
    {history?.deliveries.map(delivery => <Paper key={delivery.id} withBorder p="sm"><Stack gap={3}><Group justify="space-between"><Text size="sm">{delivery.instance_id} · {delivery.event_kind} · {delivery.provider}</Text><Badge color={delivery.status === 'sent' ? 'green' : delivery.status === 'queued' ? 'blue' : 'orange'}>{delivery.status.replaceAll('_', ' ')}</Badge></Group><Text size="xs">Destination {delivery.channel_id} · {new Date(delivery.created_at).toLocaleString()} · {delivery.attempts}/3 attempts</Text>{delivery.safe_failure && <Text size="sm">{delivery.safe_failure.replaceAll('_', ' ')}</Text>}</Stack></Paper>)}
    {!!history?.next_cursor && <Button variant="light" loading={busy} onClick={() => void older()}>Load older deliveries</Button>}
  </Stack>;
}
function ChannelForm({ channel, onSaved }: { channel: Channel | null; onSaved: () => void }) {
  const [id, setId] = useState(channel?.id || '');
  const [label, setLabel] = useState(channel?.label || '');
  const [provider, setProvider] = useState<Provider>(channel?.provider || 'discord');
  const [enabled, setEnabled] = useState(channel?.enabled || false);
  const [failed, setFailed] = useState(channel?.events.includes('job_failed') ?? true);
  const [succeeded, setSucceeded] = useState(channel?.events.includes('job_succeeded') ?? false);
  const [allowPrivate, setAllowPrivate] = useState(channel?.allow_private || false);
  const [replace, setReplace] = useState(!channel);
  const [clear, setClear] = useState(false);
  const [destination, setDestination] = useState<Destination>(empty);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const change = <K extends keyof Destination>(key: K, value: Destination[K]) => setDestination(previous => ({ ...previous, [key]: value }));
  async function save(event: React.FormEvent) {
    event.preventDefault(); setBusy(true); setError('');
    const input: Input = { label, provider, enabled: !!channel && enabled && !clear, events: [...(failed ? ['job_failed' as const] : []), ...(succeeded ? ['job_succeeded' as const] : [])], allow_private: allowPrivate, clear };
    if (replace && !clear) input.replace = provider === 'email' ? { smtp_host: destination.smtp_host, smtp_port: destination.smtp_port, smtp_user: destination.smtp_user, smtp_password: destination.smtp_password, recipient: destination.recipient, from: destination.from, tls_mode: destination.tls_mode } : { endpoint: destination.endpoint, secret: provider === 'webhook' || provider === 'ntfy' ? destination.secret : '', recipient: provider === 'telegram' ? destination.recipient : '' };
    try { await api<Channel>('/notifications/channels/' + encodeURIComponent(id), { method: 'PUT', body: JSON.stringify(input) }); setDestination(empty()); onSaved(); }
    catch (error) { setError(message(error)); } finally { setBusy(false); }
  }
  return <Paper withBorder p="md"><form onSubmit={save}><Stack>
    <Title order={2}>{channel ? 'Edit ' + channel.label : 'New notification destination'}</Title>
    {error && <Alert color="red" role="alert">{error}</Alert>}
    <TextInput label="Destination ID" description="Lowercase letter, then up to 47 lowercase letters, digits, underscores or hyphens." required pattern={'[a-z][a-z0-9_\\-]{0,47}'} value={id} disabled={!!channel} onChange={e => setId(e.currentTarget.value)} />
    <TextInput label="Destination label" required maxLength={80} value={label} onChange={e => setLabel(e.currentTarget.value)} />
    <Select label="Provider" required data={providers} value={provider} onChange={value => { setProvider(value as Provider); setReplace(true); setDestination(empty()); }} />
    <Checkbox label="Notify when an eligible job fails" checked={failed} onChange={e => setFailed(e.currentTarget.checked)} />
    <Checkbox label="Notify when an eligible job succeeds" checked={succeeded} onChange={e => setSucceeded(e.currentTarget.checked)} />
    <Checkbox label="Allow private network destination (administrator host scope)" description="Needed for self-hosted ntfy, webhooks or SMTP. Link-local metadata destinations remain blocked." checked={allowPrivate} onChange={e => setAllowPrivate(e.currentTarget.checked)} />
    {channel && <><Checkbox label="Enable future delivery to this destination" checked={enabled} disabled={clear || !channel.configured && !replace} onChange={e => setEnabled(e.currentTarget.checked)} /><Checkbox label="Replace complete stored destination and credentials" checked={replace} disabled={clear} onChange={e => { setReplace(e.currentTarget.checked); setDestination(empty()); }} /><Checkbox label="Clear stored destination and credentials, and disable delivery" checked={clear} onChange={e => { setClear(e.currentTarget.checked); setDestination(empty()); }} /></>}
    {replace && !clear && (provider === 'email' ? <>
      <TextInput label="SMTP host" required autoComplete="off" value={destination.smtp_host || ''} onChange={e => change('smtp_host', e.currentTarget.value)} />
      <NumberInput label="SMTP port" min={1} max={65535} allowDecimal={false} required value={destination.smtp_port} onChange={value => change('smtp_port', Number(value))} />
      <Select label="SMTP encryption" required value={destination.tls_mode} data={[{ value: 'starttls', label: 'Required STARTTLS' }, { value: 'tls', label: 'TLS on connection' }]} onChange={value => change('tls_mode', value as Destination['tls_mode'])} />
      <TextInput label="SMTP username (optional)" autoComplete="off" value={destination.smtp_user || ''} onChange={e => change('smtp_user', e.currentTarget.value)} />
      <PasswordInput label="SMTP password" autoComplete="new-password" value={destination.smtp_password || ''} onChange={e => change('smtp_password', e.currentTarget.value)} />
      <TextInput label="From email address" type="email" required autoComplete="off" value={destination.from || ''} onChange={e => change('from', e.currentTarget.value)} />
      <TextInput label="Recipient email address" type="email" required autoComplete="off" value={destination.recipient || ''} onChange={e => change('recipient', e.currentTarget.value)} />
    </> : <>
      <PasswordInput label={provider === 'telegram' ? 'Telegram bot sendMessage HTTPS URL' : 'Provider HTTPS endpoint (private)'} description={provider === 'ntfy' ? 'Full HTTPS topic URL, such as https://ntfy.sh/your-topic' : 'Stored privately; tokens in URL paths or query parameters are never echoed.'} required autoComplete="new-password" value={destination.endpoint || ''} onChange={e => change('endpoint', e.currentTarget.value)} />
      {(provider === 'ntfy' || provider === 'webhook') && <PasswordInput label="Bearer token (optional)" autoComplete="new-password" value={destination.secret || ''} onChange={e => change('secret', e.currentTarget.value)} />}
      {provider === 'telegram' && <PasswordInput label="Telegram chat ID" required autoComplete="new-password" value={destination.recipient || ''} onChange={e => change('recipient', e.currentTarget.value)} />}
    </>)}
    <Button type="submit" loading={busy}>{channel ? 'Save notification changes' : 'Save disabled destination'}</Button>
  </Stack></form></Paper>;
}

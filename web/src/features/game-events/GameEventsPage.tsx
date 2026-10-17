import { useEffect, useState } from 'react';
import { Alert, Anchor, Badge, Button, Group, Paper, Stack, Text, Title } from '@mantine/core';
import { Link, useParams } from 'react-router-dom';
import { api } from '../../api/client';
import type { components } from '../../api/schema';

type History = components['schemas']['GameEventPage'];
type Event = components['schemas']['GameEvent'];
const when = (value: string) => value.startsWith('0001-') ? 'Not observed' : new Date(value).toLocaleString();
const errorText = (error: unknown) => error instanceof Error ? error.message : 'Event history request failed.';

export function GameEventsPage() {
  const { instanceId = '' } = useParams();
  return <Stack gap="lg"><Anchor component={Link} to={'/instances/' + encodeURIComponent(instanceId)}>Back to server</Anchor><GameEventsPanel key={instanceId} instanceId={instanceId} /></Stack>;
}

export function GameEventsPanel({ instanceId }: { instanceId: string }) {
  const [history, setHistory] = useState<History | null>(null);
  const [request, setRequest] = useState({ after: 0, refresh: 0 });
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState('');
  useEffect(() => {
    const controller = new AbortController();
    void api<History>('/instances/' + encodeURIComponent(instanceId) + '/game-events?limit=100&after=' + request.after, { signal: controller.signal })
      .then(page => {
        if (controller.signal.aborted) return;
        setHistory(previous => {
          if (request.after === 0 || !previous) return page;
          const events = [...new Map([...previous.events, ...page.events].map(event => [event.id, event])).values()];
          return { ...page, events: events.slice(-500) };
        });
        setError('');
      })
      .catch(error => { if (!controller.signal.aborted) setError(errorText(error)); })
      .finally(() => { if (!controller.signal.aborted) setBusy(false); });
    return () => controller.abort();
  }, [instanceId, request]);
  const refresh = () => { setBusy(true); setError(''); setRequest(previous => ({ after: 0, refresh: previous.refresh + 1 })); };
  const more = () => { if (history) { setBusy(true); setError(''); setRequest(previous => ({ ...previous, after: history.next_cursor })); } };
  return <Stack gap="lg">
    <Group justify="space-between" wrap="wrap"><div><Title order={1}>Deaths &amp; advancements</Title><Text c="dimmed">Observed native log history</Text></div><Button variant="light" disabled={busy} onClick={refresh}>Refresh history</Button></Group>
    {error && <Alert color="red" role="alert">{error} Displayed records may be out of date.<Button variant="subtle" onClick={refresh} disabled={busy}>Retry history</Button></Alert>}
    <Paper withBorder p="lg"><Stack gap="sm"><Group wrap="wrap"><Title order={2}>Coverage</Title><Badge color="yellow">Partial observations</Badge><Badge color={history?.coverage.available ? 'blue' : 'gray'}>{history?.coverage.available ? 'Reviewed log patterns available' : 'Observation unavailable'}</Badge></Group>
      <Text size="sm">Events come from available server logs. Missing or disabled output can hide deaths and advancements.</Text>
      <details><summary>Coverage and source details</summary><Stack gap="xs" mt="sm">
      <Text size="sm">{history?.coverage.reason || 'Checking log coverage. Empty history does not establish that no events occurred.'}</Text>
      {history && <><Text size="sm">Configured version: {history.coverage.configured_version || 'Unknown'} · Historical emitting version: unknown</Text><Text size="sm" c="dimmed">Events last checked: {when(history.coverage.observed_at)} · Logs last checked: {when(history.coverage.logs_observed_at)}</Text><Text size="sm" c="dimmed">{history.coverage.parser_capability === 'static_reviewed_patterns' ? 'Log patterns have been reviewed.' : 'Log patterns are unverified for this version.'} Pattern review does not qualify a server or loader combination.</Text></>}
      </Stack></details>
    </Stack></Paper>
    <Paper withBorder p="lg"><Stack>
      <Title order={2}>Retained observations</Title>
      <Text size="sm">Oldest first. Times are manager ingestion observations; server dates and time zones are unknown. Names are name-only evidence and never establish UUIDs.</Text>
      {!!history?.retention_floor && <Alert color="yellow">Earlier retained events have expired. This history is incomplete.</Alert>}
      {history && !history.events.some(event => event.kind !== 'source_gap') && <Alert color="gray">No retained deaths or advancements have been observed. Missing or disabled log output can hide events.</Alert>}
      {!history && busy && <Text role="status">Loading event history…</Text>}
      {history?.events.map(event => <EventRow key={event.id} event={event} />)}
      <Text size="sm" c="dimmed">Up to 500 observations are kept on screen. Refresh returns to the beginning of retained history.</Text>
      <Button variant="light" disabled={busy || !history?.more} loading={busy && !!history} onClick={more}>Load more retained observations</Button>
      {history && !history.more && <Text size="sm" c="dimmed">End of retained history. Refresh to check for new observations.</Text>}
    </Stack></Paper>
  </Stack>;
}
function EventRow({ event }: { event: Event }) {
  return <Paper withBorder p="sm" style={{ minWidth: 0 }}><Stack gap={4}>
    <Group wrap="wrap"><Badge color={event.kind === 'source_gap' ? 'yellow' : event.kind === 'death' ? 'red' : 'green'}>{event.kind.replaceAll('_', ' ')}</Badge><Text size="sm">Observed {when(event.observed_at)}</Text></Group>
    <Text style={{ overflowWrap: 'anywhere' }}>{event.message}</Text>
    {event.kind !== 'source_gap' && <Text size="sm">{event.player.name || 'Unknown name'} · {event.player.authority.replaceAll('_', ' ')} · UUID unknown</Text>}
    <details><summary>Event source</summary><Text size="sm" mt="xs" c="dimmed" style={{ overflowWrap: 'anywhere' }}>Source: {event.source === 'redacted_native_console' ? 'Server console (redacted)' : event.source} · console line {event.source_cursor} · emitting version {event.source_version} · {event.confidence.replaceAll('_', ' ')} · parser {event.parser_version}</Text></details>
    {event.gap && <Text size="sm" c="light-dark(#7a4b00, #ffd43b)">Source coverage has gaps.</Text>}
  </Stack></Paper>;
}

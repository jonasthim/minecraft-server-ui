import { useEffect, useState } from 'react';
import { Alert, Button, Code, Loader, Paper, Stack, Table, Text, TextInput, Title } from '@mantine/core';
import { api } from '../../api/client';
import { useAuth } from './AuthProvider';
import type { APIResponse, AuditEntry } from './types';

export function AuditPage() {
  const { user } = useAuth();
  return user?.role === 'admin' ? <AuditHistory key={String(user.id) + ':' + user.role} /> : <Alert color="red">Administrator access is required to read audit history.</Alert>;
}
function AuditHistory() {
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [error, setError] = useState(''); const [more, setMore] = useState(false);
  const [busy, setBusy] = useState(true); const [filter, setFilter] = useState('');
  const [request, setRequest] = useState<{ before?: number; sequence: number }>({ sequence: 0 });
  useEffect(() => {
    const controller = new AbortController(); setBusy(true); setError('');
    void api<APIResponse<'auditList', 200>>('/audit' + (request.before ? '?before=' + request.before : ''), { signal: controller.signal }).then(value => {
      if (controller.signal.aborted) return;
      const page = value.entries ?? [];
      setEntries(old => request.before ? [...new Map([...old, ...page].map(entry => [entry.id, entry])).values()] : page);
      setMore(page.length === 100);
    }).catch(error => { if (!controller.signal.aborted) setError(error instanceof Error ? error.message : 'Could not load audit history'); }).finally(() => { if (!controller.signal.aborted) setBusy(false); });
    return () => controller.abort();
  }, [request]);
  const visible = entries.filter(entry => [entry.username, entry.action, entry.target, entry.instance_id, entry.ip, JSON.stringify(entry.details)].some(value => value?.toLowerCase().includes(filter.toLowerCase())));
  return <Stack><Title order={2}>Audit log</Title><Text c="dimmed">Recorded actions and redacted change details. Credentials and token secrets are omitted by the server.</Text><TextInput label="Filter loaded audit history" description="Matches actor, action, target, instance, IP and recorded details." value={filter} onChange={event => setFilter(event.currentTarget.value)} /><Text size="sm">Showing {visible.length} of {entries.length} loaded events. Filters apply only to loaded history; load older events to search further.</Text><Button variant="light" disabled={busy} onClick={() => setRequest(old => ({ sequence: old.sequence + 1 }))}>Refresh audit history</Button>{error && <Alert color="red" role="alert">{error}{entries.length ? ' Previously loaded entries may be stale.' : ''}</Alert>}{busy && <Loader aria-label="Loading audit history" />}<Paper withBorder p="md"><Table.ScrollContainer minWidth={650} type="native" role="region" aria-label="Activity history" tabIndex={0}><Table><Table.Thead><Table.Tr><Table.Th>Time</Table.Th><Table.Th>Actor</Table.Th><Table.Th>Action and recorded details</Table.Th><Table.Th>Target / instance</Table.Th><Table.Th>IP</Table.Th></Table.Tr></Table.Thead><Table.Tbody>{visible.map(entry => <Table.Tr key={entry.id}><Table.Td>{new Date(entry.ts).toLocaleString()}</Table.Td><Table.Td>{entry.username || 'Anonymous'}</Table.Td><Table.Td><Text size="sm">{entry.action}</Text><details><summary>Recorded details for event {entry.id}</summary>{entry.details && Object.keys(entry.details).length ? <Code block style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', maxWidth: 500 }}>{JSON.stringify(entry.details, null, 2)}</Code> : <Text size="sm">No change details were recorded.</Text>}</details></Table.Td><Table.Td>{entry.target || '—'}{entry.instance_id && <Text size="xs">Instance: {entry.instance_id}</Text>}</Table.Td><Table.Td>{entry.ip || '—'}</Table.Td></Table.Tr>)}</Table.Tbody></Table></Table.ScrollContainer>{!busy && !error && !visible.length && <Text p="md">{entries.length ? 'No loaded audit events match this filter.' : 'No audit events yet.'}</Text>}</Paper>{more && <Button variant="light" loading={busy} onClick={() => setRequest(old => ({ before: entries.at(-1)?.id, sequence: old.sequence + 1 }))}>Load older events</Button>}{!busy && !more && entries.length > 0 && <Text size="sm">End of retained audit history at the last read.</Text>}</Stack>;
}

import { useEffect, useRef, useState } from 'react';
import { Alert, Anchor, Badge, Button, Group, Paper, Select, SimpleGrid, Stack, Table, Text, Title } from '@mantine/core';
import { Link, useParams } from 'react-router-dom';
import { api } from '../../api/client';
import { startMetricsPoll } from './poll';
import type { components } from '../../api/schema';

type Page = components['schemas']['MetricsPage'];
type Point = components['schemas']['MetricPoint'];
const options = [{ value: '1', label: 'Last hour · 30 second samples' }, { value: '6', label: 'Last 6 hours · 30 second samples' }, { value: '24', label: 'Last day · hourly averages' }, { value: '168', label: 'Last 7 days · hourly averages' }, { value: '2159', label: 'Last 90 days · hourly averages' }];
const sourceLabel = (kind: string) => ({ linux_proc: 'Linux process counters', windows_process_api: 'Windows process counters', jvm_identity: 'Java process identity', instance_logical_bytes: 'Instance file sizes', task11_native_roster: 'Native player roster', sampling: 'Scheduled collection' })[kind] ?? kind.replaceAll('_', ' ');
const fieldLabel = (field: string) => ({ cpu: 'CPU', resident: 'Resident memory', private: 'Private memory', disk: 'Disk', players: 'Players' })[field] ?? field;
const bytes = (value: number | null | undefined) => value == null ? 'Unknown' : (value / 1024 / 1024).toFixed(1) + ' MiB';
const count = (value: number | null | undefined) => value == null ? 'Unknown' : Number.isInteger(value) ? String(value) : value.toFixed(1);
const date = (value: string) => value.startsWith('0001-') ? 'Not observed' : new Date(value).toLocaleString();
export function MetricsPage() {
  const { instanceId = '' } = useParams();
  return <Stack><Anchor component={Link} to={'/instances/' + encodeURIComponent(instanceId)}>Back to server</Anchor><MetricsPanel key={instanceId} instanceId={instanceId} /></Stack>;
}
export function MetricsPanel({ instanceId }: { instanceId: string }) {
  const [hours, setHours] = useState('1');
  const [data, setData] = useState<Page | null>(null);
  const [error, setError] = useState('');
  const [attempt, setAttempt] = useState(0);
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    setLoading(true); setData(null);
    return startMetricsPoll(async signal => {
      const to = new Date();
      const resolution = Number(hours) <= 6 ? 'raw' : 'hourly';
      const step = resolution === 'raw' ? 30000 : 3600000;
      const from = new Date(Math.floor((to.getTime() - Number(hours) * 3600000) / step) * step);
      const endpoint = '/instances/' + encodeURIComponent(instanceId) + '/metrics?';
      const pages: Page[] = [];
      // Every selected bucket is retained; API requests remain below 1000 each.
      for (let start = from.getTime(); start < to.getTime(); start += 999 * step) {
        signal.throwIfAborted();
        const end = Math.min(to.getTime(), start + 999 * step);
        pages.push(await api<Page>(endpoint + new URLSearchParams({ from: new Date(start).toISOString(), to: new Date(end).toISOString(), resolution }), { signal }));
      }
      signal.throwIfAborted();
      return { ...pages[0], points: pages.flatMap(part => part.points) };
    }, page => { setData(page); setError(''); setLoading(false); }, error => {
      setError(error instanceof Error ? error.message : 'Metrics could not be loaded.'); setLoading(false);
    });
  }, [instanceId, hours, attempt]);
  const latest = data?.points.at(-1);
  const rows = data?.points.toReversed() ?? [];
  const fields: { label: string; value: string }[] = [{ label: 'JVM CPU', value: latest?.cpu_percent == null ? 'Unknown' : latest.cpu_percent.toFixed(1) + '%' }, { label: 'JVM resident memory', value: bytes(latest?.resident_bytes) }, { label: 'JVM private memory', value: bytes(latest?.private_bytes) }, { label: 'Instance disk', value: bytes(latest?.disk_bytes) }, { label: data?.resolution === 'hourly' ? 'Average observed players' : 'Observed players', value: count(latest?.players) }];
  return <Stack style={{ minWidth: 0 }}>
    <Group justify="space-between"><Title order={2}>JVM metrics</Title><Button variant="light" onClick={() => setAttempt(value => value + 1)} loading={loading}>Refresh metrics</Button></Group>
    <Select label="Metric history range" data={options} value={hours} onChange={value => setHours(value || '1')} />
    {error && <Alert color="red" role="alert" title="Metrics unavailable; previously displayed values may be stale">{error}</Alert>}
    {loading && !data && <Text role="status">Loading metrics…</Text>}
    <SimpleGrid cols={{ base: 1, xs: 2, md: 3 }}>{fields.map(field => <Paper key={field.label} withBorder p="sm"><Text size="sm" c="dimmed">{field.label}</Text><Text fw={600}>{field.value}</Text></Paper>)}</SimpleGrid>
    <Text size="sm">{data?.cpu_convention || '100% CPU means one fully busy logical CPU. JVM memory is not Java heap. Unavailable observations remain unknown.'}</Text>
    <Text size="sm">Disk totals include this server’s files, logs, backups and local state. Scans have size and time limits. Shared runtimes and the manager database are excluded. Player counts come from the server roster; <Anchor component={Link} to={'/instances/' + encodeURIComponent(instanceId) + '/players'}>player history</Anchor> records observed sessions and coverage gaps.</Text>
    {latest && <Paper withBorder p="sm"><Stack gap="xs"><Group><Text fw={600}>Latest sample · {date(latest.collected_at)}</Text><Badge color={latest.gap ? 'orange' : 'green'}>{latest.gap ? 'Coverage gaps' : 'Observed'}</Badge></Group><Text size="sm">{latest.counts.samples}/{latest.expected} scheduled observations. A missing bucket may reflect manager downtime, collection limits or an unavailable JVM.</Text><details><summary>Metric sources and gaps</summary><Stack gap="xs" mt="xs">{latest.sources.map((source, index) => <Text key={index} size="sm">{fieldLabel(source.field)}: {sourceLabel(source.kind)} · {date(source.observed_at)}{source.gap_reason ? ' · ' + source.gap_reason.replaceAll('_', ' ') : ''}</Text>)}</Stack></details></Stack></Paper>}
    <Text size="sm" c="dimmed">Collection every 30 seconds. Raw retention: 7 days; hourly retention: 90 days. Hourly values average available observations only. Current hourly buckets may be incomplete.</Text>
    {data && <HistoryTable points={rows} total={data.points.length} />}
  </Stack>;
}
function HistoryTable({ points, total }: { points: Point[]; total: number }) {
  const viewport = useRef<HTMLDivElement>(null);
  return <><Text size="sm">Showing all {total} selected periods, newest first. Missing values are unknown.</Text><Group><Button variant="light" onClick={() => viewport.current?.scrollTo({ top: 0 })}>Newest observations</Button><Button variant="light" onClick={() => viewport.current?.scrollTo({ top: viewport.current.scrollHeight })}>Oldest observations</Button></Group><div ref={viewport} role="region" aria-label="Scrollable metric history" tabIndex={0} style={{ maxHeight: 440, overflowY: 'auto' }}><Table.ScrollContainer minWidth={620}><Table captionSide="top"><Table.Caption>JVM and instance metric history</Table.Caption><Table.Thead><Table.Tr><Table.Th>Observed</Table.Th><Table.Th>CPU</Table.Th><Table.Th>RSS</Table.Th><Table.Th>Disk</Table.Th><Table.Th>Players</Table.Th><Table.Th>Coverage</Table.Th></Table.Tr></Table.Thead><Table.Tbody>{points.map(point => <Table.Tr key={point.collected_at}><Table.Td>{date(point.collected_at)}</Table.Td><Table.Td>{point.cpu_percent == null ? 'Unknown' : point.cpu_percent.toFixed(1) + '%'}</Table.Td><Table.Td>{bytes(point.resident_bytes)}</Table.Td><Table.Td>{bytes(point.disk_bytes)}</Table.Td><Table.Td>{count(point.players)}</Table.Td><Table.Td>{point.counts.samples}/{point.expected}{point.gap ? ' · gaps' : ''}</Table.Td></Table.Tr>)}</Table.Tbody></Table></Table.ScrollContainer></div></>;
}

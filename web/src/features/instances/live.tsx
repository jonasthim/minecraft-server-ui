import { useEffect, useState } from 'react';
import { Badge, Group, SimpleGrid, Text } from '@mantine/core';
import { api } from '../../api/client';
import type { components } from '../../api/schema';
import { useAuth } from '../auth/AuthProvider';
import { startMetricsPoll } from '../metrics/poll';

type Schema = components['schemas'];
export type LiveObservation = { state?: Schema['LifecycleState']; snapshot?: Schema['PlayerSnapshot']; point?: Schema['MetricPoint']; observed: string; errors: string[] };
export const isRecent = (time?: string) => !!time && Number.isFinite(Date.parse(time)) && Date.now() - Date.parse(time) < 90000 && Date.parse(time) <= Date.now() + 30000;
export const exactPlayers = (live?: LiveObservation) => live?.snapshot?.known && live.snapshot.complete && !live.snapshot.gap && isRecent(live.snapshot.observed_at) && !live.errors.length ? live.snapshot.count : null;
export const memoryLabel = (value?: number | null) => value == null ? 'Unknown' : (value / 1048576).toFixed(1) + ' MiB';

export async function readLive(id: string, signal: AbortSignal): Promise<LiveObservation> {
  const path = '/instances/' + encodeURIComponent(id);
  const to = new Date();
  const query = new URLSearchParams({ from: new Date(to.getTime() - 120000).toISOString(), to: to.toISOString(), resolution: 'raw' });
  // At most three concurrent reads per server, with a bounded request lifetime.
  const bounded = AbortSignal.any([signal, AbortSignal.timeout(10000)]);
  const results = await Promise.allSettled([
    api<Schema['LifecycleState']>(path + '/lifecycle', { signal: bounded }),
    api<Schema['PlayerSnapshotResponse']>(path + '/players', { signal: bounded }),
    api<Schema['MetricsPage']>(path + '/metrics?' + query, { signal: bounded }),
  ] as const);
  signal.throwIfAborted();
  const [state, players, metrics] = results;
  return { state: state.status === 'fulfilled' ? state.value : undefined, snapshot: players.status === 'fulfilled' ? players.value.snapshot : undefined, point: metrics.status === 'fulfilled' ? metrics.value.points.at(-1) : undefined, observed: to.toISOString(), errors: results.flatMap((value, index) => value.status === 'rejected' ? [( ['Lifecycle', 'Players', 'Metrics'][index]) + ': ' + (value.reason instanceof Error ? value.reason.message : 'Unavailable')] : []) };
}
export function useLiveInstances(ids: string[]) {
  const { user } = useAuth();
  const key = JSON.stringify(ids);
  const scope = key + ':' + user?.id + ':' + user?.role;
  const [data, setData] = useState<{ scope: string; values: Record<string, LiveObservation> }>({ scope: '', values: {} });
  useEffect(() => startMetricsPoll(async signal => {
    const queue = JSON.parse(key) as string[];
    const values: Record<string, LiveObservation> = {};
    let next = 0;
    // Two workers cap a fleet refresh at six concurrent requests, independent of fleet size.
    await Promise.all(Array.from({ length: Math.min(2, queue.length) }, async () => {
      while (next < queue.length) { signal.throwIfAborted(); const id = queue[next++]; values[id] = await readLive(id, signal); }
    }));
    return values;
  }, values => setData({ scope, values }), () => {}, 15000), [key, scope]);
  return data.scope === scope ? data.values : {};
}
export function LiveFacts({ live }: { live?: LiveObservation }) {
  const point = live?.point;
  const stale = !isRecent(live?.observed) || !!live?.errors.length;
  const metricsCurrent = isRecent(point?.collected_at);
  const count = exactPlayers(live);
  return <><Group gap="xs" wrap="wrap"><Badge color={stale ? 'orange' : live?.state?.process_running ? 'forest' : 'gray'}>{stale ? 'Live state unavailable' : 'Process: ' + live?.state?.process}</Badge><Text size="sm">Readiness: {stale ? 'Unknown' : live?.state?.readiness ?? 'Unknown'}</Text></Group>
    <SimpleGrid cols={{ base: 2, md: 4 }} spacing="sm" mt="sm">
      {[['Online players', count == null ? 'Unknown' : String(count)], ['JVM CPU', point?.cpu_percent == null ? 'Unknown' : point.cpu_percent.toFixed(1) + '%'], ['JVM resident memory', memoryLabel(point?.resident_bytes)], ['Instance disk', memoryLabel(point?.disk_bytes)]].map(([label, value]) => <div key={label}><Text size="xs" c="dimmed">{label}</Text><Text fw={600}>{value}</Text></div>)}
    </SimpleGrid>
    <Text size="xs" c="dimmed" mt="sm">{point ? 'Metric sample: ' + new Date(point.collected_at).toLocaleString() + (!metricsCurrent ? ' · stale' : '') + (point.gap ? ' · coverage gaps' : '') : 'Metrics have not been observed.'} CPU: 100% is one logical CPU.</Text>
    {live?.snapshot && count == null && <Text size="xs" c="dimmed">Player coverage incomplete or stale{live.snapshot.reason ? ': ' + live.snapshot.reason : '.'}</Text>}
    {!!live?.errors.length && <Text size="sm" c="orange" role="status">Partial live data: {live.errors.join('; ')}</Text>}
  </>;
}

import { Paper, SimpleGrid, Stack, Text } from '@mantine/core';
import type { components } from '../../api/schema';

type Point = components['schemas']['MetricPoint'];
type Field = 'cpu_percent' | 'resident_bytes' | 'disk_bytes' | 'players';
const charts: { field: Field; source: string; label: string; unit: string; scale: number }[] = [
  { field: 'cpu_percent', source: 'cpu', label: 'JVM CPU', unit: '% of one logical CPU', scale: 1 },
  { field: 'resident_bytes', source: 'resident', label: 'JVM resident memory', unit: 'MiB', scale: 1048576 },
  { field: 'disk_bytes', source: 'disk', label: 'Instance disk', unit: 'MiB', scale: 1048576 },
  { field: 'players', source: 'players', label: 'Observed players', unit: 'players', scale: 1 },
];
export function MetricCharts({ points, interval }: { points: Point[]; interval: number }) {
  return <><Text size="sm" c="dimmed">Trend lines stop at missing values, source gaps and missing periods. Exact observations and sources remain available in metric history.</Text><SimpleGrid cols={{ base: 1, md: 2 }}>{charts.map(chart => {
    const valid = (point: Point) => point[chart.field] != null && !point.gap && !point.sources.some(source => source.field === chart.source && source.gap_reason);
    const times = points.map(point => Date.parse(point.collected_at));
    const first = times[0] ?? 0; const span = Math.max(1, (times.at(-1) ?? first) - first);
    const max = Math.max(1, ...points.filter(valid).map(point => (point[chart.field] ?? 0) / chart.scale));
    const segments: { x: number; y: number; point: Point }[][] = []; let current: typeof segments[number] = [];
    points.forEach((point, index) => {
      if (!valid(point) || index > 0 && times[index] - times[index - 1] > interval * 1500) { if (current.length) segments.push(current); current = []; }
      if (valid(point)) current.push({ x: 42 + (times[index] - first) / span * 496, y: 144 - (point[chart.field] ?? 0) / chart.scale / max * 124, point });
    });
    if (current.length) segments.push(current);
    return <Paper withBorder p="md" key={chart.field}><Stack gap="xs"><Text fw={600}>{chart.label}</Text><Text size="xs" c="dimmed">{chart.unit}</Text>
      <svg className="app-metric-chart" viewBox="0 0 560 180" role="img" aria-label={chart.label + ' trend; ' + segments.reduce((sum, part) => sum + part.length, 0) + ' observations in ' + segments.length + ' continuous segments'} data-field={chart.field}>
        <title>{chart.label} trend</title><path d="M42 20V144H538" fill="none" stroke="currentColor" opacity=".35" />
        <text x="2" y="24" fontSize="11" fill="currentColor">{max.toFixed(1)}</text><text x="16" y="144" fontSize="11" fill="currentColor">0</text>
        {segments.map((segment, i) => <g key={i}><polyline data-segment={i} points={segment.map(value => value.x + ',' + value.y).join(' ')} fill="none" stroke="var(--app-chart)" strokeWidth="2" />{segment.map(value => <circle key={value.point.collected_at} cx={value.x} cy={value.y} r="2" fill="var(--app-chart)"><title>{new Date(value.point.collected_at).toLocaleString()}: {((value.point[chart.field] ?? 0) / chart.scale).toFixed(1)} {chart.unit}</title></circle>)}</g>)}
        {!segments.length && <text x="280" y="90" textAnchor="middle" fontSize="13" fill="currentColor">No continuous observations available</text>}
        {!!points.length && <><text x="42" y="170" fontSize="11" fill="currentColor">{new Date(first).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</text><text x="538" y="170" textAnchor="end" fontSize="11" fill="currentColor">{new Date(times.at(-1)!).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</text></>}
      </svg>
    </Stack></Paper>;
  })}</SimpleGrid></>;
}

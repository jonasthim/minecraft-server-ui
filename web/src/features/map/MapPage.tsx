import { useEffect, useReducer, useRef, useState } from 'react';
import { Alert, Anchor, Badge, Button, Checkbox, Group, NumberInput, Paper, Select, SimpleGrid, Stack, Text, Title } from '@mantine/core';
import { Link, useParams } from 'react-router-dom';
import { api } from '../../api/client';
import type { components } from '../../api/schema';
import { startVisiblePoll } from './poll';
import { initialSelection, reduceSelection } from './selection';

type Summary = components['schemas']['MapSummary'];
type View = components['schemas']['MapView'];
type Tile = components['schemas']['MapTile'];
const when = (value?: string) => !value || value.startsWith('0001-') ? 'Not observed' : new Date(value).toLocaleString();
const message = (error: unknown) => error instanceof Error ? error.message : 'Map observation failed.';
const stateColor: Record<string, string> = { missing: '#303d47', stale: '#ad7c25', unsupported: '#576074', corrupt: '#a84646', unknown: '#8b4bb4' };
export function MapPage() {
  const { instanceId = '' } = useParams();
  return <Stack><Anchor component={Link} to={'/instances/' + encodeURIComponent(instanceId)}>Back to server</Anchor><MapPanel key={instanceId} instanceId={instanceId} /></Stack>;
}
export function MapPanel({ instanceId }: { instanceId: string }) {
  const endpoint = '/instances/' + encodeURIComponent(instanceId) + '/map';
  const [{ summary, view, dimension }, dispatchSelection] = useReducer(reduceSelection, initialSelection);
  const setDimension = (value: string) => dispatchSelection({ type: 'dimension', value });
  const [center, setCenter] = useState({ x: 0, z: 0 });
  const [height, setHeight] = useState<number | string>('');
  const [zoom, setZoom] = useState(1);
  const [touchDrag, setTouchDrag] = useState(false);
  const [offset, setOffset] = useState({ x: 0, z: 0 });
  const drag = useRef<{ id: number; x: number; y: number; width: number; height: number; zoom: number } | null>(null);
  const pendingCenter = useRef<{ x: number; z: number } | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [error, setError] = useState('');
  const [summaryError, setSummaryError] = useState('');
  const canvas = useRef<HTMLCanvasElement>(null);
  useEffect(() => {
    drag.current = null; setOffset({ x: 0, z: 0 }); setZoom(1);
    setCenter(pendingCenter.current || { x: 0, z: 0 }); pendingCenter.current = null;
  }, [dimension]);
  useEffect(() => {
    const element = canvas.current;
    if (!element) return;
    const wheel = (event: WheelEvent) => {
      if (document.activeElement !== element && !event.ctrlKey) return;
      event.preventDefault();
      drag.current = null; setOffset({ x: 0, z: 0 });
      if (event.deltaY) setZoom(value => event.deltaY < 0 ? Math.min(4, value * 2) : Math.max(1, value / 2));
    };
    element.addEventListener('wheel', wheel, { passive: false });
    return () => element.removeEventListener('wheel', wheel);
  }, []);
  useEffect(() => startVisiblePoll(signal => api<Summary>(endpoint, { signal }), value => {
    dispatchSelection({ type: 'summary', value }); setSummaryError('');
  }, error => setSummaryError(message(error))), [endpoint, attempt]);
  useEffect(() => {
    if (!dimension) return;
    const query = new URLSearchParams({ dimension, chunk_x: String(center.x), chunk_z: String(center.z) });
    if (typeof height === 'number') query.set('max_y', String(Math.trunc(height)));
    return startVisiblePoll(signal => api<View>(endpoint + '/view?' + query, { signal }), value => { dispatchSelection({ type: 'view', dimension, value }); setError(''); }, error => setError(message(error)));
  }, [endpoint, dimension, center.x, center.z, height, attempt]);
  useEffect(() => {
    const context = canvas.current?.getContext('2d');
    if (!context) return;
    context.fillStyle = '#17252b'; context.fillRect(0, 0, 480, 480);
    if (!view) return;
    context.save(); context.translate(offset.x, offset.z); context.translate(240, 240); context.scale(zoom, zoom); context.translate(-240, -240);
    for (const tile of view.tiles) {
      const left = (tile.chunk_x - center.x + 1) * 160, top = (tile.chunk_z - center.z + 1) * 160;
      if (tile.state === 'ready' && tile.cells.length === 256) tile.cells.forEach((cell, index) => {
        context.fillStyle = cell.empty ? '#17252b' : cell.known && /^#[0-9a-f]{6}$/i.test(cell.color) ? cell.color : '#a855cf';
        context.fillRect(left + index % 16 * 10, top + Math.floor(index / 16) * 10, 10, 10);
      });
      else { context.fillStyle = stateColor[tile.state] || stateColor.unknown; context.fillRect(left, top, 160, 160); context.strokeStyle = '#ffffff66'; for (let x = 0; x < 160; x += 20) { context.beginPath(); context.moveTo(left + x, top); context.lineTo(left, top + x); context.stroke(); } }
      context.strokeStyle = '#00000044'; context.strokeRect(left, top, 160, 160);
    }
    for (const marker of view.native.positions) {
      if (marker.dimension !== dimension) continue;
      const x = (marker.x - (center.x - 1) * 16) * 10, z = (marker.z - (center.z - 1) * 16) * 10;
      if (x < 0 || z < 0 || x >= 480 || z >= 480) continue;
      context.beginPath(); context.arc(x, z, 5 / zoom, 0, Math.PI * 2); context.fillStyle = '#ffffff'; context.fill(); context.lineWidth = 2 / zoom; context.strokeStyle = '#17252b'; context.stroke();
      context.fillStyle = '#ffffff'; context.font = `${12 / zoom}px sans-serif`; context.fillText(marker.player.name || marker.player.uuid || 'Observed player', x + 7 / zoom, z - 7 / zoom);
    }
    context.restore();
  }, [view, center.x, center.z, zoom, dimension, offset]);
  const resetView = () => { dispatchSelection({ type: 'clear_view' }); setError(''); };
  const cancelDrag = () => { drag.current = null; setOffset({ x: 0, z: 0 }); };
  const pan = (x: number, z: number) => { cancelDrag(); resetView(); setCenter(previous => ({ x: Math.max(-1874999, Math.min(1874998, previous.x + x)), z: Math.max(-1874999, Math.min(1874998, previous.z + z)) })); };
  const counts = view?.tiles.reduce<Record<string, number>>((all, tile) => { all[tile.state] = (all[tile.state] || 0) + 1; return all; }, {}) || {};
  const unknown = view?.tiles.flatMap(tile => tile.cells).filter(cell => !cell.empty && !cell.known).length || 0;
  const described = Object.entries(counts).map(([state, count]) => count + ' ' + state).join(', ');
  return <Stack gap="md" style={{ minWidth: 0 }}>
    <Group justify="space-between"><div><Title order={1}>Saved map</Title><Text c="dimmed">Terrain from saved chunks</Text></div><Button variant="light" onClick={() => setAttempt(value => value + 1)}>Retry / refresh</Button></Group>
    {(summaryError || error) && <Alert color="red" role="alert" title="Observation failed; displayed data may be stale">{summaryError || error}</Alert>}
    {summary && <><Group><Badge color={summary.state === 'ready' ? 'green' : 'orange'}>{summary.state}</Badge><Text size="sm">{summary.version_name || summary.discovery.minecraft_version} · DataVersion {summary.data_version}</Text></Group>{summary.reason && <Alert color="orange">{summary.reason}</Alert>}</>}
    {summary?.state === 'missing' && <Text>No saved world yet. Terrain appears after this server creates a save; no game process is started by viewing this page.</Text>}
    <SimpleGrid cols={{ base: 1, sm: 2 }}>
      <Select label="Saved dimension" placeholder="No discovered dimensions" searchable value={dimension || null} onChange={value => { resetView(); setDimension(value || ''); }} data={(summary?.discovery.dimensions || []).map(d => ({ value: d.id, label: d.id + ' · ' + d.kind + ' · ' + d.state }))} />
      <NumberInput label="Inclusive maximum Y" description="Blank: surface. Highest non-air at or below Y, not an exact plane." placeholder="Surface" min={-2032} max={2031} allowDecimal={false} value={height} onChange={value => { resetView(); setHeight(value); }} />
    </SimpleGrid>
    <Paper withBorder p="sm"><Stack gap="sm">
      <Group justify="space-between"><Text size="sm">Center chunk {center.x}, {center.z} · {48 / zoom} blocks across · north ↑</Text><Select aria-label="Map zoom" value={String(zoom)} onChange={value => { cancelDrag(); setZoom(Number(value || 1)); }} data={['1', '2', '4'].map(value => ({ value, label: value + '× zoom' }))} w={112} /></Group>
      <Group gap="xs" justify="center"><Button size="xs" variant="default" onClick={() => pan(-1, 0)}>West</Button><Button size="xs" variant="default" onClick={() => pan(0, -1)}>North</Button><Button size="xs" variant="default" onClick={() => pan(0, 1)}>South</Button><Button size="xs" variant="default" onClick={() => pan(1, 0)}>East</Button></Group>
      <Text size="xs" c="dimmed">Drag to pan saved chunks. Focus the map (or hold Ctrl) and scroll to zoom. Arrow keys and the buttons also navigate. Touch dragging is optional so page scrolling remains available.</Text>
      <Checkbox label="Enable touch map dragging" checked={touchDrag} onChange={event => { cancelDrag(); setTouchDrag(event.currentTarget.checked); }} />
      <canvas ref={canvas} width={480} height={480} tabIndex={0} role="img" aria-label="Saved terrain map. Use arrow keys to pan and plus or minus to zoom." aria-describedby="map-summary" style={{ width: '100%', height: 'auto', aspectRatio: '1', touchAction: touchDrag ? 'none' : 'pan-y', cursor: 'grab', maxWidth: 640, alignSelf: 'center', imageRendering: 'pixelated', borderRadius: 4 }} onPointerDown={event => {
        if (event.button !== 0 || (event.pointerType === 'touch' && !touchDrag)) return;
        const rect = event.currentTarget.getBoundingClientRect();
        event.currentTarget.focus({ preventScroll: true });
        event.currentTarget.setPointerCapture(event.pointerId);
        drag.current = { id: event.pointerId, x: event.clientX, y: event.clientY, width: rect.width, height: rect.height, zoom };
      }} onPointerMove={event => {
        const start = drag.current;
        if (!start || start.id !== event.pointerId) return;
        setOffset({ x: (event.clientX - start.x) * 480 / start.width, z: (event.clientY - start.y) * 480 / start.height });
      }} onPointerUp={event => {
        const start = drag.current;
        if (!start || start.id !== event.pointerId) return;
        const x = Math.round(-(event.clientX - start.x) * 3 / start.width / start.zoom);
        const z = Math.round(-(event.clientY - start.y) * 3 / start.height / start.zoom);
        cancelDrag();
        if (x || z) pan(x, z);
        if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId);
      }} onPointerCancel={cancelDrag} onLostPointerCapture={cancelDrag} onBlur={cancelDrag} onKeyDown={event => {
        const direction: Record<string, [number, number]> = { ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1] };
        if (direction[event.key]) { event.preventDefault(); pan(...direction[event.key]); }
        if (event.key === '+' || event.key === '=') { event.preventDefault(); cancelDrag(); setZoom(value => Math.min(4, value * 2)); }
        if (event.key === '-') { event.preventDefault(); cancelDrag(); setZoom(value => Math.max(1, value / 2)); }
      }}>Saved terrain is described in the map summary and chunk details below.</canvas>
      <Text id="map-summary" size="sm" role="status">{view ? described + '. ' + unknown + ' unknown non-air cells (purple).' : 'Waiting for the selected saved area.'} Empty cells are dark. Hatched chunks have no validated terrain; their state appears below.</Text>
      <Text size="xs" c="dimmed">Five-second active sampling; hidden pages pause. Last view sample: {when(view?.sampled_at)}. Saved source age is independent of this observation time.</Text>
    </Stack></Paper>
    <Alert color="yellow" title="Saved terrain can lag gameplay">{summary?.consistency || 'Saved map data is not a live or coherent point-in-time world snapshot.'}</Alert>
    {summary?.discovery.minecraft_version.startsWith('1.12.') && <Alert color="orange" title="Minecraft 1.12.2 limits">Legacy terrain uses numeric block IDs and Y 0–255. Mod registry names cannot be recovered from these chunks; unresolved blocks stay purple with their numeric labels. Native targeted position reads are unavailable for this command version.</Alert>}
    <Paper withBorder p="sm"><Stack gap="xs"><Title order={2} size="h4">Native player observations</Title>
      <Text size="sm">{view?.native.snapshot.known ? view.native.snapshot.count + ' players at the last native observation' : 'Current player presence unknown'} · {when(view?.native.snapshot.observed_at)}</Text>
      <Text size="sm">{view?.native.reason || 'Waiting for native observation. Identity/count alone never establishes coordinates or a dimension.'}</Text>
      {view?.native.gap && <Alert color="orange">Position coverage has gaps. Missing markers do not prove that no players are present.</Alert>}
      {view?.native.truncated && <Text size="sm">Coverage is capped at the first 16 observed players.</Text>}
      {view?.native.positions.map(marker => <Stack gap={2} key={marker.player.uuid || marker.player.name}>
        <Text size="sm" fw={600}>{marker.player.name || marker.player.uuid} · {marker.dimension}</Text>
        <Text size="xs">{marker.x.toFixed(1)}, {marker.y.toFixed(1)}, {marker.z.toFixed(1)} · {when(marker.observed_at)} · {marker.source} · {marker.player.authority}</Text>
        {typeof height === 'number' && marker.y > height && <Text size="xs">Player is above the selected terrain ceiling.</Text>}
        {summary?.discovery.dimensions.some(d => d.id === marker.dimension) && <Button size="xs" variant="light" onClick={() => { resetView(); cancelDrag(); const target = { x: Math.max(-1874999, Math.min(1874998, Math.floor(marker.x / 16))), z: Math.max(-1874999, Math.min(1874998, Math.floor(marker.z / 16))) }; if (marker.dimension !== dimension) pendingCenter.current = target; setDimension(marker.dimension); setCenter(target); }}>Center on {marker.player.name || 'player'}</Button>}
      </Stack>)}
      <Text size="xs" c="dimmed">Targeted native data-get reads need no add-on. Field shapes are statically checked for 1.21.9 and 26.3; actual game interoperability is not yet qualified. Other versions and unfamiliar/localized/plugin feedback remain unknown. Native coordinates and saved terrain have different ages.</Text>
    </Stack></Paper>
    {view && <details><summary>Chunk states and saved source evidence ({view.tiles.length})</summary><Stack mt="sm">{view.tiles.map(tile => <TileEvidence key={tile.chunk_x + ':' + tile.chunk_z} tile={tile} />)}</Stack></details>}
  </Stack>;
}
function TileEvidence({ tile }: { tile: Tile }) {
  const unknown = [...new Set(tile.cells.filter(cell => !cell.empty && !cell.known).map(cell => cell.block))].slice(0, 8);
  return <Paper withBorder p="sm" style={{ overflowWrap: 'anywhere' }}><Text fw={600}>Chunk {tile.chunk_x}, {tile.chunk_z} · {tile.state}</Text>{tile.reason && <Text size="sm">{tile.reason}</Text>}<Text size="xs">DataVersion {tile.data_version} · {tile.format || 'No format'} · {tile.codec || 'No codec'}</Text><Text size="xs">Containing file modified: {when(tile.source_mtime)} · Header chunk save time (game clock): {when(tile.chunk_saved_at)} · Manager observed: {when(tile.sampled_at)}</Text>{unknown.length > 0 && <Text size="xs">Unknown non-air: {unknown.join(', ')}</Text>}</Paper>;
}

import { useCallback, useEffect, useRef, useState } from 'react';
import { Alert, Anchor, Badge, Button, Group, Loader, Paper, SimpleGrid, Stack, Text, Title } from '@mantine/core';
import { Link, useParams } from 'react-router-dom';
import { api } from '../../api/client';
import type { components } from '../../api/schema';
import { useAuth } from '../auth/AuthProvider';

type Discovery = components['schemas']['WorldDiscovery'];
type DiscoveryResponse = components['schemas']['WorldDiscoveryResponse'];
const errorText = (error: unknown) => error instanceof Error ? error.message : 'Could not load the saved world.';

export function WorldsPage() {
  const { instanceId } = useParams();
  return <Stack gap="lg"><Anchor component={Link} to={'/instances/' + encodeURIComponent(instanceId ?? '')}>Back to server</Anchor><WorldsPanel instanceId={instanceId ?? ''} /></Stack>;
}

export function WorldsPanel({ instanceId }: { instanceId: string }) {
  const { user } = useAuth();
  const [world, setWorld] = useState<Discovery | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const frame = useRef<HTMLIFrameElement>(null);
  const frameName = 'world-download-' + instanceId;
  const endpoint = '/instances/' + encodeURIComponent(instanceId) + '/world';
  const load = useCallback(async (signal?: AbortSignal) => {
    setLoading(true); setError('');
    try { const value = await api<DiscoveryResponse>('/instances/' + encodeURIComponent(instanceId) + '/world', { signal }); if (!signal?.aborted) setWorld(value.world); }
    catch (error) { if (!signal?.aborted) setError(errorText(error)); }
    finally { if (!signal?.aborted) setLoading(false); }
  }, [instanceId]);
  useEffect(() => { setWorld(null); setNotice(''); const controller = new AbortController(); void load(controller.signal); return () => controller.abort(); }, [load]);
  function downloadResult() {
    // A failed same-origin attachment request renders JSON in the target frame.
    // Successful attachments remain browser-managed and are never buffered in JS.
    const doc = frame.current?.contentDocument;
    if (!doc || doc.contentType !== 'application/json') return;
    try { const result: { error?: { message?: string } } = JSON.parse(doc.body.textContent ?? ''); if (result.error) { setNotice(''); setError(result.error.message ?? 'World download failed.'); } }
    catch { setNotice(''); setError('World download failed. Refresh the saved world and try again.'); }
  }
  const canDownload = user?.role === 'operator' || user?.role === 'admin';
  return <Stack gap="md" style={{ minWidth: 0 }}>
    <Group justify="space-between" wrap="wrap"><Title order={2}>Saved world</Title><Button variant="default" loading={loading} onClick={() => void load()}>Refresh world</Button></Group>
    {error && <Alert color="red" role="alert">{error}</Alert>}
    {notice && <Alert color="blue" role="status">{notice}</Alert>}
    {loading && !world && <Loader aria-label="Discovering saved world" />}
    {world && <>
      <Paper withBorder p="md"><Stack gap="sm">
        <Group justify="space-between" wrap="wrap"><Title order={3} size="h4" style={{ overflowWrap: 'anywhere', minWidth: 0 }}>{world.level_name}</Title><Badge color={world.state === 'present' ? 'green' : world.state === 'absent' ? 'gray' : 'orange'}>{world.state === 'present' ? 'Save found' : world.state}</Badge></Group>
        <Text size="sm" c="dimmed">{world.flavor} · Minecraft {world.minecraft_version} · {world.layout.replaceAll('_', ' ')}</Text>
        {world.state === 'absent' && <Text>No saved world was found for this server. A world appears after the server creates its first save.</Text>}
        {(world.state === 'ambiguous' || world.state === 'unsupported') && <Alert color="orange" title="Download unavailable">The saved layout cannot be identified safely. Review the world details below before trying again.</Alert>}
        <Text size="sm">{world.provenance}</Text>
      </Stack></Paper>
      <Alert color="yellow" title="Live copy has consistency limits">{world.consistency}</Alert>
      {world.dimensions.length > 0 && <Stack gap="sm"><Title order={3} size="h4">Dimensions</Title><SimpleGrid cols={{ base: 1, sm: 2 }} spacing="sm">{world.dimensions.map(dimension => <Paper withBorder p="md" key={dimension.id + dimension.path} style={{ minWidth: 0 }}><Stack gap={4}><Group justify="space-between" wrap="wrap"><Text fw={600} style={{ overflowWrap: 'anywhere', minWidth: 0 }}>{dimension.id}</Text><Badge variant="light" color={dimension.state === 'present' ? 'green' : 'gray'}>{dimension.state === 'present' ? 'Saved' : 'No saved data'}</Badge></Group><Text size="sm" c="dimmed" style={{ overflowWrap: 'anywhere' }}>{dimension.path}</Text><Text size="xs" c="dimmed">{dimension.kind === 'unknown' ? 'Unknown mod dimension identity and format' : dimension.kind + ' dimension'}</Text></Stack></Paper>)}</SimpleGrid></Stack>}
      {world.omitted.length > 0 && <Alert color="orange" title="Files omitted from this download"><Text size="sm">These paths are excluded. Check this list before using the copy.</Text><ul style={{ paddingLeft: 20, marginBottom: 0 }}>{world.omitted.map(item => <li key={item.path} style={{ overflowWrap: 'anywhere' }}>{item.path} — {item.reason}</li>)}</ul></Alert>}
      {world.warnings.length > 0 && <Stack gap={4}>{world.warnings.map(warning => <Text key={warning} size="sm" c="dimmed">{warning}</Text>)}</Stack>}
      <Paper withBorder p="md"><Stack gap="sm"><Title order={3} size="h4">Download world</Title><Text size="sm">The ZIP preserves the saved folder layout, including associated dimensions and player saves. A manifest lists file hashes, omissions, and consistency limits. Maximum source size: 16 GiB.</Text>{canDownload ? <Button component="a" href={'/api/v1' + endpoint + '/download'} target={frameName} disabled={world.state !== 'present'} aria-disabled={world.state !== 'present'} style={{ alignSelf: 'flex-start', maxWidth: '100%' }} onClick={event => { if (world.state !== 'present') { event.preventDefault(); return; } setError(''); setNotice('Preparing the world ZIP. This can take several minutes. Check your browser downloads for progress; a failed capture will appear here.'); }}>Download world ZIP</Button> : <Text size="sm" c="dimmed">An operator or administrator can download this world.</Text>}</Stack></Paper>
    </>}
    <iframe ref={frame} name={frameName} title="World download response" hidden onLoad={downloadResult} onError={() => { setNotice(''); setError('Could not connect to the world download. Try again.'); }} />
  </Stack>;
}

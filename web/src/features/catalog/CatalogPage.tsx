import { useEffect, useRef, useState, type FormEvent } from 'react';
import { Alert, Anchor, Badge, Button, Group, Loader, Paper, PasswordInput, Select, SimpleGrid, Stack, Text, TextInput, Title } from '@mantine/core';
import { api } from '../../api/client';
import type { components } from '../../api/schema';
import { useAuth } from '../auth/AuthProvider';

type Availability = components['schemas']['CatalogAvailability'];
type Project = components['schemas']['CatalogProjectMetadata'];
type Version = components['schemas']['CatalogVersionMetadata'];
type SearchResult = components['schemas']['CatalogSearchResult'];
type VersionPage = components['schemas']['CatalogVersionPage'];
type Provider = Availability['provider'];
type Kind = Project['kind'];
const label = (value: Provider) => ({ modrinth: 'Modrinth', curseforge: 'CurseForge', hangar: 'Hangar' })[value];
const failure = (error: unknown) => error instanceof Error ? error.message : 'Catalog request failed.';

export function CatalogPage() {
  const { user } = useAuth();
  const [providers, setProviders] = useState<Availability[]>([]);
  const [provider, setProvider] = useState<Provider>('modrinth');
  const [kind, setKind] = useState<Kind>('mod');
  const [text, setText] = useState('');
  const [minecraft, setMinecraft] = useState('');
  const [loader, setLoader] = useState('');
  const [result, setResult] = useState<SearchResult | null>(null);
  const [project, setProject] = useState<Project | null>(null);
  const [versions, setVersions] = useState<VersionPage | null>(null);
  const [detail, setDetail] = useState<Version | null>(null);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const [keyBusy, setKeyBusy] = useState(false);
  const [key, setKey] = useState('');
  const request = useRef<AbortController | null>(null);
  const pageRef = useRef<HTMLDivElement>(null);
  const detailRef = useRef<HTMLDivElement>(null);
  useEffect(() => { if (detail) detailRef.current?.focus(); }, [detail]);
  const [submitted, setSubmitted] = useState('');
  useEffect(() => {
    const controller = new AbortController();
    void api<components['schemas']['CatalogProviders']>('/catalog/providers', { signal: controller.signal }).then(value => setProviders(value.providers)).catch(error => { if (!controller.signal.aborted) setError(failure(error)); });
    return () => { controller.abort(); request.current?.abort(); };
  }, []);
  function startRequest() { request.current?.abort(); const controller = new AbortController(); request.current = controller; setBusy(true); setError(''); return controller; }
  function filters(offset: number) { return new URLSearchParams({ kind, q: text, minecraft, loader, offset: String(offset), limit: '20' }).toString(); }
  function changeProvider(value: Provider) { request.current?.abort(); setBusy(false); setProvider(value); setKind(value === 'hangar' ? 'plugin' : 'mod'); setLoader(''); setResult(null); setProject(null); setVersions(null); setDetail(null); setError(''); }
  async function search(event?: FormEvent, offset = 0) {
    event?.preventDefault(); const controller = startRequest(); const query = filters(offset); setProject(null); setVersions(null); setDetail(null);
    try { const value = await api<SearchResult>('/catalog/' + provider + '/search?' + query, { signal: controller.signal }); if (!controller.signal.aborted) { setResult(value); setSubmitted(filters(0)); pageRef.current?.focus(); } }
    catch (error) { if (!controller.signal.aborted) { setResult(null); setError(failure(error)); } }
    finally { if (!controller.signal.aborted) setBusy(false); }
  }
  async function openVersions(selected: Project, offset = 0) {
    const controller = startRequest(); setProject(selected); setDetail(null); setVersions(null);
    try { const value = await api<VersionPage>('/catalog/' + selected.source + '/projects/' + encodeURIComponent(selected.id) + '/versions?' + filters(offset), { signal: controller.signal }); if (!controller.signal.aborted) { setVersions(value); pageRef.current?.focus(); } }
    catch (error) { if (!controller.signal.aborted) setError(failure(error)); }
    finally { if (!controller.signal.aborted) setBusy(false); }
  }
  async function serverPack(version: Version) {
    const controller = startRequest();
    try { const value = await api<Version>('/catalog/curseforge/projects/' + encodeURIComponent(version.project_id) + '/versions/' + encodeURIComponent(version.server_pack_file_id), { signal: controller.signal }); if (!controller.signal.aborted) { setDetail(value); pageRef.current?.focus(); } }
    catch (error) { if (!controller.signal.aborted) setError(failure(error)); }
    finally { if (!controller.signal.aborted) setBusy(false); }
  }
  async function saveKey(clear: boolean) {
    setKeyBusy(true); setError(''); setNotice('');
    try { const value = await api<Availability>('/catalog/curseforge-key', { method: 'PUT', body: JSON.stringify({ key: clear ? '' : key }) }); setProviders(previous => previous.map(item => item.provider === 'curseforge' ? value : item)); setKey(''); setNotice(clear ? 'CurseForge key cleared.' : 'Private key saved. Publisher access is checked when searching.'); }
    catch (error) { setError(failure(error)); }
    finally { setKeyBusy(false); }
  }
  const available = providers.find(item => item.provider === provider)?.enabled ?? false;
  const curseforge = providers.find(item => item.provider === 'curseforge');
  const kinds = provider === 'hangar' ? [{ value: 'plugin', label: 'Plugins' }] : [{ value: 'mod', label: 'Mods' }, ...(provider === 'modrinth' ? [{ value: 'plugin', label: 'Plugins' }] : []), { value: 'modpack', label: 'Modpacks' }];
  return <Stack gap="lg" style={{ minWidth: 0 }}>
    <Title order={2}>Content catalogs</Title>
    <Text c="dimmed">Browse publisher releases for mods, plugins and modpacks. Publisher compatibility declarations are discovery evidence; they do not establish tested support.</Text>
    {error && <Alert color="red" role="alert">{error}</Alert>}
    {notice && <Alert color="green" role="status">{notice}</Alert>}
    {curseforge && !curseforge.enabled && <Alert color="yellow" title="CurseForge unavailable">{curseforge.reason}</Alert>}
    {user?.role === 'admin' && <Paper withBorder p="md"><Stack gap="sm"><Title order={3} size="h4">Private CurseForge key</Title><Text size="sm">{curseforge?.enabled ? 'A private key is configured. Its value is never returned.' : 'No key is configured.'} Supply your own approved API key. It is stored in the private manager database.</Text><PasswordInput label="Replace private API key" autoComplete="new-password" maxLength={512} value={key} onChange={event => setKey(event.currentTarget.value)} /><Group wrap="wrap"><Button loading={keyBusy} disabled={!key || keyBusy} onClick={() => void saveKey(false)}>Save private key</Button><Button variant="default" disabled={!curseforge?.enabled || keyBusy} onClick={() => void saveKey(true)}>Clear key</Button></Group></Stack></Paper>}
    <Paper withBorder p="md"><form onSubmit={event => void search(event)}><Stack gap="md"><SimpleGrid cols={{ base: 1, sm: 2 }}><Select label="Provider" allowDeselect={false} data={(['modrinth', 'curseforge', 'hangar'] as const).map(value => ({ value, label: label(value), disabled: value === 'curseforge' && !curseforge?.enabled }))} value={provider} onChange={value => { if (value) changeProvider(value as Provider); }} /><Select label="Content type" allowDeselect={false} data={kinds} value={kind} onChange={value => { if (value) { setKind(value as Kind); setResult(null); setProject(null); setVersions(null); setDetail(null); } }} /><TextInput label="Search publishers" maxLength={200} value={text} onChange={event => setText(event.currentTarget.value)} /><TextInput label="Minecraft version (optional)" placeholder="1.21.1" maxLength={160} value={minecraft} onChange={event => setMinecraft(event.currentTarget.value)} /><Select label="Loader or platform (optional)" clearable data={provider === 'hangar' ? ['paper'] : kind === 'plugin' ? ['paper', 'bukkit', 'spigot', 'purpur'] : ['fabric', 'forge', 'neoforge', 'quilt']} value={loader || null} onChange={value => setLoader(value ?? '')} /></SimpleGrid><Button type="submit" loading={busy} disabled={!available} style={{ alignSelf: 'flex-start' }}>Search catalog</Button></Stack></form></Paper>
    <div ref={pageRef} tabIndex={-1} aria-label="Catalog results" style={{ outlineOffset: 4 }}>
      <Stack gap="md">
        {busy && <Loader aria-label="Loading publisher metadata" />}
        {result && !project && <><Text size="sm" c="dimmed">{result.total} publisher results · fetched {new Date(result.fetched_at).toLocaleString()}</Text>{!result.projects.length && <Text>No matching projects were returned.</Text>}{result.projects.map(item => <Paper withBorder p="md" key={item.source + item.id}><Stack gap="sm"><Group justify="space-between" wrap="wrap"><Title order={3} size="h4" style={{ overflowWrap: 'anywhere' }}>{item.title}</Title><Badge>{item.kind}</Badge></Group><Text size="sm">{item.description}</Text><Group wrap="wrap"><Anchor href={item.official_url} target="_blank" rel="noopener noreferrer">Official publisher page</Anchor><Button variant="light" disabled={busy} onClick={() => void openVersions(item)}>Browse {item.title} releases</Button></Group></Stack></Paper>)}<Group wrap="wrap"><Button variant="default" disabled={busy || result.offset === 0 || submitted !== filters(0)} onClick={() => void search(undefined, Math.max(0, result.offset - result.limit))}>Previous projects</Button><Button variant="default" disabled={busy || result.offset + result.limit >= result.total || submitted !== filters(0)} onClick={() => void search(undefined, result.offset + result.limit)}>Next projects</Button></Group></>}
        {project && <><Group wrap="wrap"><Button variant="default" disabled={busy} onClick={() => { setProject(null); setVersions(null); setDetail(null); }}>Back to projects</Button><Title order={3} size="h4" style={{ overflowWrap: 'anywhere' }}>{project.title}</Title></Group>{versions && !detail && <><Text size="sm" c="dimmed">{versions.total} publisher releases · fetched {new Date(versions.fetched_at).toLocaleString()}</Text>{!versions.versions.length && <Text>No matching releases were returned.</Text>}{versions.versions.map(version => <Paper withBorder p="md" key={version.id}><Stack gap="sm"><Text fw={600} style={{ overflowWrap: 'anywhere' }}>{version.name}</Text><Text size="sm">Server environment: {version.compatibility.environment.replaceAll('_', ' ')} · {version.compatibility.server_side}</Text><Button variant="light" disabled={busy} onClick={() => { setDetail(version); pageRef.current?.focus(); }}>Inspect {version.name}</Button></Stack></Paper>)}<Group wrap="wrap"><Button variant="default" disabled={busy || !versions.offset} onClick={() => void openVersions(project, Math.max(0, versions.offset - versions.limit))}>Previous releases</Button><Button variant="default" disabled={busy || versions.offset + versions.limit >= versions.total} onClick={() => void openVersions(project, versions.offset + versions.limit)}>Next releases</Button></Group></>}</>}
        {detail && <Paper ref={detailRef} tabIndex={-1} withBorder p="md"><Stack gap="sm"><Button variant="default" onClick={() => setDetail(null)}>Back to releases</Button><Title order={3} size="h4" style={{ overflowWrap: 'anywhere' }}>{detail.name}</Title><Anchor href={detail.official_url} target="_blank" rel="noopener noreferrer">Official release page</Anchor><Text size="sm">{detail.compatibility.evidence}</Text><Text size="sm" style={{ overflowWrap: 'anywhere' }}>Minecraft: {detail.compatibility.minecraft.join(', ') || 'Unknown'} · Loaders: {detail.compatibility.loaders.join(', ') || 'Unknown'}</Text><Alert color={detail.compatibility.server_side === 'unsupported' ? 'red' : 'yellow'}>Publisher server compatibility: {detail.compatibility.server_side}. Tested support: unqualified.</Alert>{kind === 'modpack' && <Alert color="yellow">Pack contents require a server-item selection and compatibility review before application. A generic client pack is not a runnable server pack.</Alert>}{detail.server_pack_file_id && <Button variant="light" disabled={busy} onClick={() => void serverPack(detail)}>Inspect attached server pack {detail.server_pack_file_id}</Button>}{detail.files.map(file => <Paper withBorder p="sm" key={file.id}><Stack gap={4}><Text fw={600} style={{ overflowWrap: 'anywhere' }}>{file.filename || 'External artifact'}</Text><Text size="sm">{file.size.toLocaleString()} bytes · {file.role || 'Publisher artifact'}</Text>{file.manual_reason && <Text c="orange" size="sm">Manual resolution required: {file.manual_reason}</Text>}{Object.entries(file.hashes).map(([algorithm, hash]) => <Text key={algorithm} size="xs" style={{ overflowWrap: 'anywhere' }}>{algorithm}: {hash}</Text>)}</Stack></Paper>)}{detail.dependencies.length > 0 && <><Title order={4}>Dependencies</Title>{detail.dependencies.map((dependency, index) => <Text size="sm" key={index} style={{ overflowWrap: 'anywhere' }}>{dependency.name || dependency.project_id || dependency.version_id || 'Unresolved dependency'} · {dependency.relation}{dependency.required ? ' (required)' : ''}{dependency.manual_url && <> · <Anchor href={dependency.manual_url} target="_blank" rel="noopener noreferrer">Manual resolution required</Anchor></>}</Text>)}</>}</Stack></Paper>}
      </Stack>
    </div>
  </Stack>;
}

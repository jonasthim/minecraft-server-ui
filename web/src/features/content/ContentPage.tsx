import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react';
import { Alert, Anchor, Badge, Button, Checkbox, FileInput, Group, Loader, Paper, Select, SimpleGrid, Stack, Text, Textarea, TextInput, Title } from '@mantine/core';
import { Link, useParams } from 'react-router-dom';
import { api } from '../../api/client';
import type { components } from '../../api/schema';
import { useAuth } from '../auth/AuthProvider';

type Manifest = components['schemas']['ContentManifest'];
type Mutation = components['schemas']['ContentMutation'];
type Preview = components['schemas']['ContentPreview'];
type Upload = components['schemas']['ContentUpload'];
type Listing = components['schemas']['ContentConfigListing'];
type ConfigText = components['schemas']['ContentConfigText'];
type Installed = components['schemas']['ContentInstalled'];
type Job = components['schemas']['Job'];
type Project = components['schemas']['CatalogProjectMetadata'];
type Version = components['schemas']['CatalogVersionMetadata'];
type Provider = components['schemas']['CatalogAvailability']['provider'];
const failure = (error: unknown) => error instanceof Error ? error.message : 'Content operation failed.';
const running = (job: Job | null) => job?.state === 'queued' || job?.state === 'running';

export function ContentPage() {
  const { instanceId = '' } = useParams();
  return <Stack gap="lg"><Anchor component={Link} to={'/instances/' + encodeURIComponent(instanceId)}>Back to server</Anchor><ContentPanel key={instanceId} instanceId={instanceId} /></Stack>;
}

export function ContentPanel({ instanceId }: { instanceId: string }) {
  const { user } = useAuth();
  const canOperate = user?.role === 'operator' || user?.role === 'admin';
  const admin = user?.role === 'admin';
  const endpoint = '/instances/' + encodeURIComponent(instanceId) + '/content';
  const [manifest, setManifest] = useState<Manifest | null>(null);
  const [listing, setListing] = useState<Listing | null>(null);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const [preview, setPreview] = useState<Preview | null>(null);
  const [lastMutation, setLastMutation] = useState<Mutation | null>(null);
  const [job, setJob] = useState<Job | null>(null);
  const [provider, setProvider] = useState<Provider>('modrinth');
  const [searchText, setSearchText] = useState('');
  const [projects, setProjects] = useState<Project[] | null>(null);
  const [versions, setVersions] = useState<Version[] | null>(null);
  const [project, setProject] = useState<Project | null>(null);
  const [providers, setProviders] = useState<components['schemas']['CatalogAvailability'][]>([]);
  const [updateItem, setUpdateItem] = useState<Installed | null>(null);
  const [file, setFile] = useState<File | null>(null);
  const [upload, setUpload] = useState<Upload | null>(null);
  const [trial, setTrial] = useState(false);
  const [editor, setEditor] = useState<ConfigText | null>(null);
  const [text, setText] = useState('');
  const previewRef = useRef<HTMLDivElement>(null);
  const catalogRef = useRef<HTMLDivElement>(null);
  const active = busy || running(job);
  const kind: 'mod' | 'plugin' = manifest?.tuple.loader === 'paper' ? 'plugin' : 'mod';
  const compatibleKind = manifest ? ['fabric', 'forge', 'neoforge', 'paper'].includes(manifest.tuple.loader) : false;
  const refresh = useCallback(async (signal?: AbortSignal) => {
    const [nextManifest, nextListing] = await Promise.all([api<Manifest>(endpoint, { signal }), api<Listing>(endpoint + '/config', { signal })]);
    if (!signal?.aborted) { setManifest(nextManifest); setListing(nextListing); }
  }, [endpoint]);
  useEffect(() => {
    const controller = new AbortController();
    void refresh(controller.signal).catch(error => { if (!controller.signal.aborted) setError(failure(error)); });
    void api<components['schemas']['CatalogProviders']>('/catalog/providers', { signal: controller.signal }).then(value => { if (!controller.signal.aborted) setProviders(value.providers); }).catch(error => { if (!controller.signal.aborted) setError(failure(error)); });
    return () => controller.abort();
  }, [refresh]);
  useEffect(() => { if (preview) previewRef.current?.focus(); }, [preview]);
  useEffect(() => {
    if (!job || !running(job)) return;
    const controller = new AbortController();
    const timer = window.setInterval(() => {
      void api<{ job: Job }>('/jobs/' + job.id, { signal: controller.signal }).then(async value => {
        if (controller.signal.aborted) return;
        setJob(value.job);
        if (value.job.state === 'succeeded') { setNotice('Content operation completed and saved files were verified.'); setPreview(null); setUpload(null); setFile(null); await refresh(controller.signal); if (lastMutation?.action === 'config_write') { setEditor(null); setText(''); } }
        if (value.job.state === 'failed' || value.job.state === 'cancelled') { setError(value.job.error || 'Content operation did not complete. Inspect the job output and refresh before retrying.'); await refresh(controller.signal); }
      }).catch(error => { if (!controller.signal.aborted) setError(failure(error)); });
    }, 1000);
    return () => { window.clearInterval(timer); controller.abort(); };
  }, [job, lastMutation, refresh]);
  async function perform(action: () => Promise<void>) {
    setBusy(true); setError(''); setNotice('');
    try { await action(); } catch (error) { setError(failure(error)); } finally { setBusy(false); }
  }
  async function createPreview(mutation: Mutation) {
    if (preview) { await api(endpoint + '/previews/' + preview.id, { method: 'DELETE' }); setPreview(null); }
    const value = await api<Preview>(endpoint + '/previews', { method: 'POST', body: JSON.stringify(mutation) });
    setLastMutation(mutation); setPreview(value);
  }
  async function search(event?: FormEvent) {
    event?.preventDefault(); await perform(async () => {
      if (!manifest) return;
      setProject(null); setVersions(null);
      const query = new URLSearchParams({ kind, q: searchText, minecraft: manifest.tuple.minecraft, loader: manifest.tuple.loader, limit: '20' });
      const value = await api<components['schemas']['CatalogSearchResult']>('/catalog/' + provider + '/search?' + query);
      setProjects(value.projects); catalogRef.current?.focus();
    });
  }
  async function releases(selected: Project) {
    await perform(async () => {
      if (!manifest) return;
      setProject(selected); setVersions(null);
      const query = new URLSearchParams({ kind, minecraft: manifest.tuple.minecraft, loader: manifest.tuple.loader, limit: '20' });
      const value = await api<components['schemas']['CatalogVersionPage']>('/catalog/' + selected.source + '/projects/' + encodeURIComponent(selected.id) + '/versions?' + query);
      setVersions(value.versions); catalogRef.current?.focus();
    });
  }
  async function startUpdate(item: Installed) {
    setUpdateItem(item); setUpload(null); setFile(null); setTrial(false); setProjects(null); setVersions(null); setProject(null);
    if (item.selection) {
      setProvider(item.selection.provider);
      await perform(async () => {
        const selected = await api<Project>('/catalog/' + item.selection!.provider + '/projects/' + encodeURIComponent(item.selection!.project_id));
        setProject(selected);
        const value = await api<components['schemas']['CatalogVersionPage']>('/catalog/' + item.selection!.provider + '/projects/' + encodeURIComponent(item.selection!.project_id) + '/versions?' + new URLSearchParams({ kind, minecraft: manifest!.tuple.minecraft, loader: manifest!.tuple.loader, limit: '20' }));
        setVersions(value.versions); catalogRef.current?.focus();
      });
    } else { setNotice('Choose an administrator upload to update this unknown item.'); catalogRef.current?.focus(); }
  }
  async function apply() {
    if (!preview) return;
    await perform(async () => { const value = await api<components['schemas']['ContentJobResponse']>(endpoint + '/previews/' + preview.id + '/apply', { method: 'POST' }); setJob(value.job); setNotice('Operation queued. The instance must already be stopped; inspect job output for progress.'); });
  }
  async function openConfig(path: string) {
    await perform(async () => { const value = await api<ConfigText>(endpoint + '/config/text?' + new URLSearchParams({ path })); setEditor(value); setText(value.text); });
  }
  return <Stack gap="lg" style={{ minWidth: 0 }}>
    <Group justify="space-between" wrap="wrap"><Title order={2}>Mods, plugins and configuration</Title><Button variant="default" disabled={running(job)} aria-disabled={active} aria-busy={busy} onClick={() => { if (!active) void perform(() => refresh()); }}>Refresh content</Button></Group>
    {error && <Alert color="red" role="alert">{error}</Alert>}
    {notice && <Alert color="blue" role="status">{notice}</Alert>}
    {!manifest && !error && <Loader aria-label="Loading content" />}
    <Alert color="yellow">Install, update, enable, disable, uninstall and configuration edits require a stopped server. Stop the server from its controls before applying. Addon settings remain in place when managed JARs change.</Alert>
    {job && <Paper withBorder p="sm"><Stack gap="xs"><Text style={{ overflowWrap: 'anywhere' }}>Operation: {job.state} · {job.phase || 'waiting'}</Text><Anchor component={Link} to="/jobs">Open jobs and output</Anchor></Stack></Paper>}
    {manifest && <>
      <Text c="dimmed" size="sm">{manifest.tuple.loader} {manifest.tuple.loader_version} · Minecraft {manifest.tuple.minecraft} · Game compatibility remains unqualified.</Text>
      {!!manifest.automatic_blockers.length && <Alert color="orange" title="Automatic updates blocked"><Stack gap={4}>{manifest.automatic_blockers.map((blocker, i) => <Text size="sm" key={i} style={{ overflowWrap: 'anywhere' }}>{blocker}</Text>)}</Stack></Alert>}
      <Stack gap="sm"><Title order={3}>Installed content</Title>{!manifest.items.length && <Text>No managed mods or plugins are installed.</Text>}{manifest.items.map(item => <Paper withBorder p="md" key={item.id}><Stack gap="sm"><Group justify="space-between" wrap="wrap"><Text fw={600} style={{ overflowWrap: 'anywhere' }}>{item.name}</Text><Group gap="xs" wrap="wrap"><Badge color={item.enabled ? 'green' : 'gray'}>{item.enabled ? 'Enabled' : 'Disabled'}</Badge>{item.unknown && <Badge color="orange">Unknown trial</Badge>}{item.pinned && <Badge variant="light">Version pinned</Badge>}</Group></Group><Text size="sm" style={{ overflowWrap: 'anywhere' }}>{item.kind} · {item.version || 'Unknown version'} · {item.selection?.provider ?? 'Administrator upload'}</Text><Text size="xs" style={{ overflowWrap: 'anywhere' }}>SHA-256 {item.sha256}</Text>{canOperate && <Group wrap="wrap"><Button variant="light" disabled={active || (!item.selection && !admin)} onClick={() => void startUpdate(item)}>Update {item.name}</Button><Button variant="default" disabled={active} onClick={() => void perform(() => createPreview({ action: item.enabled ? 'disable' : 'enable', content_id: item.id }))}>{item.enabled ? 'Disable' : 'Enable'} {item.name}</Button><Button color="red" variant="subtle" disabled={active} onClick={() => void perform(() => createPreview({ action: 'uninstall', content_id: item.id }))}>Uninstall {item.name}</Button></Group>}</Stack></Paper>)}</Stack>
      {!compatibleKind && <Alert color="blue">This server type does not accept managed mods or plugins. Choose the required server type through server installation controls.</Alert>}
      {compatibleKind && <Paper withBorder p="md"><Stack gap="md"><div ref={catalogRef} tabIndex={-1}><Title order={3}>{updateItem ? 'Update ' + updateItem.name : 'Browse ' + (kind === 'mod' ? 'mods' : 'plugins')}</Title></div>{updateItem && <Button variant="default" disabled={active} onClick={() => { setUpdateItem(null); setProject(null); setVersions(null); }}>Cancel update selection</Button>}
        <form onSubmit={event => void search(event)}><Stack gap="sm"><SimpleGrid cols={{ base: 1, sm: 2 }}><Select label="Content provider" value={provider} allowDeselect={false} disabled={active || !!updateItem?.selection} data={providers.filter(value => kind === 'plugin' ? value.provider !== 'curseforge' : value.provider !== 'hangar').map(value => ({ value: value.provider, label: value.provider, disabled: !value.enabled }))} onChange={value => { if (value) { setProvider(value as Provider); setProjects(null); setVersions(null); setProject(null); } }} /><TextInput label="Search content" value={searchText} maxLength={200} disabled={active || !!updateItem?.selection} onChange={event => setSearchText(event.currentTarget.value)} /></SimpleGrid><Button type="submit" disabled={active || !!updateItem?.selection || !providers.find(value => value.provider === provider)?.enabled}>Search compatible releases</Button></Stack></form>
        {projects && !project && <Stack gap="sm"><Text size="sm" c="dimmed">First {projects.length} results. Refine the search for another project.</Text>{projects.map(item => <Button variant="light" key={item.id} disabled={active} onClick={() => void releases(item)} styles={{ label: { whiteSpace: 'normal', overflowWrap: 'anywhere' } }}>{item.title}</Button>)}{!projects.length && <Text>No matching projects.</Text>}</Stack>}
        {project && <Stack gap="sm"><Group wrap="wrap"><Text fw={600} style={{ overflowWrap: 'anywhere' }}>{project.title}</Text><Anchor href={project.official_url} target="_blank" rel="noopener noreferrer">Official project</Anchor><Button variant="subtle" disabled={active} onClick={() => { setProject(null); setVersions(null); }}>Back to results</Button></Group>{versions && <Text size="sm" c="dimmed">First {versions.length} matching releases. Publisher declarations still require descriptor and dependency checks.</Text>}{versions?.map(version => <Paper withBorder p="sm" key={version.id}><Stack gap="sm"><Text fw={600} style={{ overflowWrap: 'anywhere' }}>{version.name}</Text>{version.files.map(artifact => <Stack key={artifact.id} gap={4}><Text size="sm" style={{ overflowWrap: 'anywhere' }}>{artifact.filename} · {artifact.size.toLocaleString()} bytes</Text>{artifact.manual_reason && <Text c="orange" size="sm">{artifact.manual_reason}</Text>}{canOperate && <Button variant="light" disabled={active} onClick={() => void perform(() => createPreview({ action: updateItem ? 'update' : 'install', kind, content_id: updateItem?.id, selection: { provider: project.source, project_id: project.id, version_id: version.id, file_id: artifact.id } }))}>Preview {updateItem ? 'update' : 'installation'} of {artifact.filename}</Button>}</Stack>)}</Stack></Paper>)}</Stack>}
        {admin && <Stack gap="sm"><Title order={4}>Administrator JAR upload</Title><FileInput label="Select a mod or plugin JAR" accept=".jar" value={file} disabled={active} clearable onChange={value => { setFile(value); setUpload(null); setTrial(false); }} /><Button variant="default" disabled={active || !file} onClick={() => void perform(async () => { if (!file) return; if (file.size > 2147483648) throw new Error('JAR exceeds 2 GiB limit.'); const value = await api<Upload>(endpoint + '/uploads?' + new URLSearchParams({ filename: file.name }), { method: 'POST', headers: { 'Content-Type': 'application/octet-stream' }, body: file }); setUpload(value); setNotice('JAR staged without executing or installing it.'); })}>Stage upload</Button>{upload && <><Text size="sm" style={{ overflowWrap: 'anywhere' }}>{upload.filename} · {upload.size.toLocaleString()} bytes · SHA-256 {upload.sha256}</Text>{preview?.manual ? <Button disabled={active} onClick={() => void perform(() => createPreview({ ...(lastMutation ?? { action: 'install', kind }), selection: preview.manual!.selection, upload_id: upload.id, trial: false }))}>Verify upload against publisher digest</Button> : <><Checkbox label={'Trial this unknown upload only against Minecraft ' + manifest.tuple.minecraft + ' / ' + manifest.tuple.loader + ' ' + manifest.tuple.loader_version + '. It remains unknown and blocks automatic upgrades.'} checked={trial} disabled={active} onChange={event => setTrial(event.currentTarget.checked)} /><Button disabled={active || !trial} onClick={() => void perform(() => createPreview({ action: updateItem ? 'update' : 'install', content_id: updateItem?.id, kind, upload_id: upload.id, trial }))}>Preview unknown trial</Button></>}</>}<Button variant="subtle" disabled={active} onClick={() => void perform(async () => { await api(endpoint + '/uploads', { method: 'DELETE' }); setUpload(null); setNotice('Unused actor-owned staging cleared. Previews and recovery references were retained.'); })}>Clear unused upload staging</Button></Stack>}
      </Stack></Paper>}
    </>}
    {preview && <Paper ref={previewRef} tabIndex={-1} withBorder p="md"><Stack gap="sm"><Title order={3}>Review {preview.action.replaceAll('_', ' ')}</Title><Text size="sm">Target: Minecraft {preview.tuple.minecraft} / {preview.tuple.loader} {preview.tuple.loader_version}</Text>{preview.changes.map((change, i) => <Text size="sm" key={i} style={{ overflowWrap: 'anywhere' }}>{change.sha256 ? 'Write ' : 'Remove '}{change.path}</Text>)}{preview.blockers.map((blocker, i) => <Alert key={i} color="orange">{blocker}</Alert>)}{preview.dependencies.map((dependency, i) => <Text size="sm" key={i} style={{ overflowWrap: 'anywhere' }}>{dependency.name || dependency.project_id || dependency.version_id} · {dependency.relation}{dependency.required ? ' (required)' : ''}</Text>)}{preview.manual && <Stack gap={4}><Anchor href={preview.manual.official_url} target="_blank" rel="noopener noreferrer">Official manual download</Anchor>{Object.entries(preview.manual.hashes).map(([algorithm, value]) => <Text size="xs" key={algorithm} style={{ overflowWrap: 'anywhere' }}>{algorithm}: {value}</Text>)}<Text size="sm">An administrator can stage the published file above and verify its digest to continue.</Text></Stack>}<Group wrap="wrap">{canOperate && <Button disabled={active || !!preview.blockers.length || !!preview.manual} onClick={() => void apply()}>Apply while stopped</Button>}<Button variant="default" disabled={active} onClick={() => void perform(async () => { await api(endpoint + '/previews/' + preview.id, { method: 'DELETE' }); setPreview(null); await refresh(); })}>Discard preview</Button></Group></Stack></Paper>}
    <Paper withBorder p="md"><Stack gap="md"><Title order={3}>Addon configuration</Title><Text size="sm" c="dimmed">Allowlisted text files in config, defaultconfigs and plugin data folders. Server properties use the server settings screen. Maximum 1 MiB per file.</Text>{listing?.truncated && <Alert color="yellow">Listing reached its 1000-entry limit. Additional files are not shown.</Alert>}{!!listing?.unsupported && <Text size="sm">{listing.unsupported} unsupported or unsafe entries were omitted.</Text>}{listing && !listing.files.length && <Text>No editable addon text files found. Addons may create settings after their first launch.</Text>}{listing?.files.map(item => <Group key={item.path} justify="space-between" wrap="wrap"><Text size="sm" style={{ overflowWrap: 'anywhere', minWidth: 0 }}>{item.path} · {item.size.toLocaleString()} bytes</Text>{canOperate && <Button variant="subtle" disabled={active} onClick={() => void openConfig(item.path)}>Edit {item.path}</Button>}</Group>)}{!canOperate && <Text size="sm">Configuration contents require operator access.</Text>}{editor && canOperate && <Stack gap="sm"><Textarea label={'Edit ' + editor.path} description="Whole-file UTF-8 text. Preserve unrelated keys and comments; another change will require a fresh read." value={text} disabled={active} minRows={12} autosize maxRows={30} onChange={event => setText(event.currentTarget.value)} styles={{ input: { fontFamily: 'monospace', overflowWrap: 'anywhere' } }} /><Group wrap="wrap"><Button disabled={active || text === editor.text} onClick={() => void perform(() => createPreview({ action: 'config_write', config_path: editor.path, expected_sha256: editor.sha256, text }))}>Preview configuration save</Button><Button variant="default" disabled={active} onClick={() => void openConfig(editor.path)}>Reload saved text</Button><Button variant="subtle" disabled={active} onClick={() => { setEditor(null); setText(''); }}>Close editor</Button></Group></Stack>}</Stack></Paper>
  </Stack>;
}

import { CatalogPagination, useCatalogBrowser } from './CatalogBrowser';
import { jobHref } from '../jobs';
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
type Provider = components['schemas']['CatalogAvailability']['provider'];
const failure = (error: unknown) => error instanceof Error ? error.message : 'Content operation failed.';
const running = (job: Job | null) => job?.state === 'queued' || job?.state === 'running';

export function ContentPage() {
  const { instanceId = '' } = useParams();
  return <Stack gap="lg"><Anchor component={Link} to={'/instances/' + encodeURIComponent(instanceId)}>Back to server</Anchor><ContentPanel key={instanceId} instanceId={instanceId} /></Stack>;
}

export function ContentPanel({ instanceId }: { instanceId: string }) {
  const { user } = useAuth();
  return <ContentPanelState key={instanceId + ':' + user?.id + ':' + user?.role} instanceId={instanceId} />;
}

function ContentPanelState({ instanceId }: { instanceId: string }) {
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
  const [parents, setParents] = useState<{ mutation: Mutation; project: Project | null; offset: number; exactVersionId?: string }[]>([]);
  const [providers, setProviders] = useState<components['schemas']['CatalogAvailability'][]>([]);
  const [updateItem, setUpdateItem] = useState<Installed | null>(null);
  const [file, setFile] = useState<File | null>(null);
  const [upload, setUpload] = useState<Upload | null>(null);
  const [trial, setTrial] = useState(false);
  const [editor, setEditor] = useState<ConfigText | null>(null);
  const [text, setText] = useState('');
  const previewRef = useRef<HTMLDivElement>(null);
  const catalogRef = useRef<HTMLDivElement>(null);
  const previousTuple = useRef<string | null>(null);
  const mutationActive = busy || running(job);
  const kind: 'mod' | 'plugin' = manifest?.tuple.loader === 'paper' ? 'plugin' : 'mod';
  const compatibleKind = manifest ? ['fabric', 'forge', 'neoforge', 'paper'].includes(manifest.tuple.loader) : false;
  const browser = useCatalogBrowser(instanceId + ':' + provider + ':' + JSON.stringify(manifest?.tuple), { kind, minecraft: manifest?.tuple.minecraft, loader: manifest?.tuple.loader });
  const { project, versions } = browser;
  const projects = browser.result?.projects;
  const active = mutationActive || browser.busy;
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
  const tupleKey = manifest ? JSON.stringify(manifest.tuple) : null;
  useEffect(() => {
    if (previousTuple.current && previousTuple.current !== tupleKey) { setPreview(null); setLastMutation(null); setParents([]); setUpdateItem(null); setUpload(null); setFile(null); setTrial(false); setEditor(null); setText(''); }
    previousTuple.current = tupleKey;
  }, [tupleKey]);
  useEffect(() => { if (preview) previewRef.current?.focus(); }, [preview]);
  useEffect(() => {
    if (!job || !running(job)) return;
    const controller = new AbortController();
    let polling = false;
    const timer = window.setInterval(() => {
      if (polling) return; polling = true;
      void api<{ job: Job }>('/jobs/' + job.id, { signal: controller.signal }).then(async value => {
        if (controller.signal.aborted) return;
        if (!running(value.job)) { try { await refresh(controller.signal); } catch (error) { if (!controller.signal.aborted) setError('Job finished, but content refresh failed: ' + failure(error)); } }
        if (controller.signal.aborted) return;
        setJob(value.job);
        if (value.job.state === 'succeeded') { setNotice('Content operation completed and saved files were verified.'); setPreview(null); setUpload(null); setFile(null); if (lastMutation?.action === 'config_write') { setEditor(null); setText(''); } }
        if (value.job.state === 'failed' || value.job.state === 'cancelled') { setError(value.job.error || 'Content operation did not complete. Inspect the job output and refresh before retrying.'); }
      }).catch(error => { if (!controller.signal.aborted) setError(failure(error)); }).finally(() => { polling = false; });
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
  async function search(event?: FormEvent) { event?.preventDefault(); await browser.search(provider, searchText); catalogRef.current?.focus(); }
  async function releases(selected: Project) { await browser.releases(selected); catalogRef.current?.focus(); }
  async function startUpdate(item: Installed) {
    setUpdateItem(item); setParents([]); setUpload(null); setFile(null); setTrial(false); browser.reset();
    if (item.selection) {
      // Changing provider resets discovery first; lookup starts after that reset.
      setProvider(item.selection.provider);
      setUpdateLookup(item.selection);
    } else { setNotice('Choose an administrator upload to update this unknown item.'); catalogRef.current?.focus(); }
  }
  const [updateLookup, setUpdateLookup] = useState<Installed['selection'] | null>(null);
  useEffect(() => { if (updateLookup) { void browser.identity(updateLookup.provider, updateLookup.project_id); setUpdateLookup(null); } }, [updateLookup, browser]);
  async function dependencyPlan(dependency: Preview['dependencies'][number]) {
    if (!preview || !lastMutation || !['modrinth', 'curseforge', 'hangar'].includes(dependency.provider) || !dependency.project_id) return;
    if ([...parents.map(parent => parent.mutation.selection), lastMutation.selection].some(selection => selection?.provider === dependency.provider && selection.project_id === dependency.project_id)) { setError('Dependency cycle detected. Return to the parent and resolve the publisher metadata before continuing.'); return; }
    const existing = manifest?.items.filter(item => item.selection?.provider === dependency.provider && item.selection.project_id === dependency.project_id) ?? [];
    if (existing.length > 1) { setError('Multiple installed entries have this publisher identity. Resolve the duplicate identity before selecting a dependency update.'); return; }
    const parent = { mutation: lastMutation, project, offset: browser.page?.offset ?? 0, exactVersionId: browser.exact?.id };
    await perform(async () => {
      // A blocked preview is retained server state and blocks automatic updates.
      // Only leave it after deletion succeeds; a failure keeps the parent retryable.
      await api(endpoint + '/previews/' + encodeURIComponent(preview.id), { method: 'DELETE' });
      setParents(previous => [...previous, parent]);
      setPreview(null); setLastMutation(null); setUpdateItem(existing[0] ?? null); setUpload(null); setFile(null); setTrial(false);
      await browser.identity(dependency.provider as Provider, dependency.project_id, dependency.version_id || undefined);
      catalogRef.current?.focus();
    });
  }
  async function enableDependency(item: Installed) {
    if (!lastMutation) return;
    const parent = { mutation: lastMutation, project, offset: browser.page?.offset ?? 0, exactVersionId: browser.exact?.id };
    await perform(async () => { await createPreview({ action: 'enable', content_id: item.id }); setParents(previous => [...previous, parent]); });
  }
  async function returnToParent() {
    const parent = parents.at(-1); if (!parent) return;
    await perform(async () => { await refresh(); await createPreview(parent.mutation); setParents(previous => previous.slice(0, -1)); setUpdateItem(manifest?.items.find(item => item.id === parent.mutation.content_id) ?? null); if (parent.project) { if (parent.exactVersionId) await browser.identity(parent.project.source, parent.project.id, parent.exactVersionId); else await browser.releases(parent.project, parent.offset); } else browser.back(); });
  }
  async function apply() {
    if (!preview || !canOperate || active || preview.blockers.length > 0 || preview.manual) return;
    await perform(async () => { const value = await api<components['schemas']['ContentJobResponse']>(endpoint + '/previews/' + preview.id + '/apply', { method: 'POST' }); setJob(value.job); setNotice('Operation queued. The instance must already be stopped; inspect job output for progress.'); });
  }
  async function openConfig(path: string) {
    await perform(async () => { const value = await api<ConfigText>(endpoint + '/config/text?' + new URLSearchParams({ path })); setEditor(value); setText(value.text); });
  }
  return <Stack gap="lg" style={{ minWidth: 0 }}>
    <Group justify="space-between" wrap="wrap"><Title order={2}>Mods, plugins and configuration</Title><Button variant="default" disabled={mutationActive} aria-busy={busy} onClick={() => { if (!mutationActive) void perform(() => refresh()); }}>Refresh content</Button></Group>
    {(error || browser.error) && <Alert color="red" role="alert">{error || browser.error}</Alert>}
    {notice && <Alert color="blue" role="status">{notice}</Alert>}
    {!manifest && !error && <Loader aria-label="Loading content" />}
    <Alert color="yellow">Install, update, enable, disable, uninstall and configuration edits require a stopped server. Stop the server from its controls before applying. Addon settings remain in place when managed JARs change.</Alert>
    {job && <Paper withBorder p="sm"><Stack gap="xs"><Text style={{ overflowWrap: 'anywhere' }}>Operation: {job.state} · {job.phase || 'waiting'}</Text><Anchor component={Link} to={jobHref(job.id)}>Open jobs and output</Anchor></Stack></Paper>}
    {manifest && <>
      <Text c="dimmed" size="sm">{manifest.tuple.loader} {manifest.tuple.loader_version} · Minecraft {manifest.tuple.minecraft} · Game compatibility remains unqualified.</Text>
      {!!manifest.automatic_blockers.length && <Alert color="orange" title="Automatic updates blocked"><Stack gap={4}>{manifest.automatic_blockers.map((blocker, i) => <Text size="sm" key={i} style={{ overflowWrap: 'anywhere' }}>{blocker}</Text>)}</Stack></Alert>}
      <Stack gap="sm"><Title order={3}>Installed content</Title>{!manifest.items.length && <Text>No managed mods or plugins are installed.</Text>}{manifest.items.map(item => <Paper withBorder p="md" key={item.id}><Stack gap="sm"><Group justify="space-between" wrap="wrap"><Text fw={600} style={{ overflowWrap: 'anywhere' }}>{item.name}</Text><Group gap="xs" wrap="wrap"><Badge color={item.enabled ? 'green' : 'gray'}>{item.enabled ? 'Enabled' : 'Disabled'}</Badge>{item.unknown && <Badge color="orange">Unknown trial</Badge>}{item.pinned && <Badge variant="light">Version pinned</Badge>}</Group></Group><Text size="sm" style={{ overflowWrap: 'anywhere' }}>{item.kind} · {item.version || 'Unknown version'} · {item.selection?.provider ?? 'Administrator upload'}</Text><Text size="xs" style={{ overflowWrap: 'anywhere' }}>SHA-256 {item.sha256}</Text>{canOperate && <Group wrap="wrap"><Button variant="light" disabled={active || (!item.selection && !admin)} onClick={() => void startUpdate(item)}>Update {item.name}</Button><Button variant="default" disabled={active} onClick={() => void perform(() => createPreview({ action: item.enabled ? 'disable' : 'enable', content_id: item.id }))}>{item.enabled ? 'Disable' : 'Enable'} {item.name}</Button><Button color="red" variant="subtle" disabled={active} onClick={() => void perform(() => createPreview({ action: 'uninstall', content_id: item.id }))}>Uninstall {item.name}</Button></Group>}</Stack></Paper>)}</Stack>
      {!compatibleKind && <Alert color="blue">This server type does not accept managed mods or plugins. Choose the required server type through server installation controls.</Alert>}
      {parents.length > 0 && <Alert color="blue" title="Dependency plan"><Stack gap="sm"><Text size="sm">Parent: {parents.at(-1)?.mutation.selection ? Object.values(parents.at(-1)!.mutation.selection!).join(' / ') : parents.at(-1)?.mutation.content_id || 'saved mutation'}. Review each exact dependency candidate, preview its checks and explicitly apply it while stopped. Then return to re-check the parent against the installed manifest. No dependency is installed automatically.</Text><Button disabled={active} onClick={() => void returnToParent()}>Return and re-preview parent</Button></Stack></Alert>}
      {compatibleKind && <Paper withBorder p="md"><Stack gap="md"><div ref={catalogRef} tabIndex={-1}><Title order={3}>{updateItem ? 'Update ' + updateItem.name : 'Browse ' + (kind === 'mod' ? 'mods' : 'plugins')}</Title></div>{updateItem && <Button variant="default" disabled={active} onClick={() => { setUpdateItem(null); browser.back(); }}>Cancel update selection</Button>}
        <form onSubmit={event => void search(event)}><Stack gap="sm"><SimpleGrid cols={{ base: 1, sm: 2 }}><Select label="Content provider" value={provider} allowDeselect={false} disabled={mutationActive || !!updateItem?.selection || parents.length > 0} data={providers.filter(value => kind === 'plugin' ? value.provider !== 'curseforge' : value.provider !== 'hangar').map(value => ({ value: value.provider, label: value.provider, disabled: !value.enabled }))} onChange={value => { if (value) { browser.reset(); setProvider(value as Provider); } }} /><TextInput label="Search content" value={searchText} maxLength={200} disabled={mutationActive || !!updateItem?.selection || parents.length > 0} onChange={event => setSearchText(event.currentTarget.value)} /></SimpleGrid><Button type="submit" disabled={mutationActive || !!updateItem?.selection || parents.length > 0 || !providers.find(value => value.provider === provider)?.enabled}>Search compatible releases</Button></Stack></form>
        {browser.busy && <Loader aria-label="Loading publisher metadata" />}
        {projects && !project && <Stack gap="sm"><Text size="sm" c="dimmed">Search results for this server tuple.</Text>{projects.map(item => <Button variant="light" key={item.id} disabled={active} onClick={() => void releases(item)} styles={{ label: { whiteSpace: 'normal', overflowWrap: 'anywhere' } }}>{item.title}</Button>)}{!projects.length && <Text>No matching projects.</Text>}{browser.result && <CatalogPagination page={browser.result} count={projects.length} noun="projects" busy={active} onPage={offset => void browser.searchPage(offset)} />}</Stack>}
        {project && <Stack gap="sm"><Group wrap="wrap"><Text fw={600} style={{ overflowWrap: 'anywhere' }}>{project.title}</Text><Anchor href={project.official_url} target="_blank" rel="noopener noreferrer">Official project</Anchor><Button variant="subtle" disabled={active} onClick={() => { browser.back(); }}>Back to results</Button></Group>{versions && <Text size="sm" c="dimmed">Publisher declarations still require descriptor and dependency checks. Choose one exact file for preview.</Text>}{browser.exact && <Text size="sm">Exact required publisher release: {browser.exact.id}. Other versions cannot satisfy this declared identity.</Text>}{browser.page && <CatalogPagination page={browser.page} count={browser.page.versions.length} noun="releases" busy={active} onPage={offset => void browser.releases(project, offset)} />}{versions?.length === 0 && <Text>No matching releases.</Text>}{versions?.map(version => <Paper withBorder p="sm" key={version.id}><Stack gap="sm">{browser.exact && (!version.compatibility.minecraft.includes(manifest.tuple.minecraft) || !version.compatibility.loaders.includes(manifest.tuple.loader) || version.compatibility.server_side === 'unsupported') && <Alert color="orange">The exact required release does not declare compatibility with this server tuple. Installation remains blocked; choose another parent release or resolve publisher metadata.</Alert>}<Text fw={600} style={{ overflowWrap: 'anywhere' }}>{version.name}</Text>{version.files.map(artifact => <Stack key={artifact.id} gap={4}><Text size="sm" style={{ overflowWrap: 'anywhere' }}>{artifact.filename} · {artifact.size.toLocaleString()} bytes</Text>{artifact.manual_reason && <Text c="orange" size="sm">{artifact.manual_reason}</Text>}{canOperate && <Button variant="light" disabled={active || !!browser.exact && (!version.compatibility.minecraft.includes(manifest.tuple.minecraft) || !version.compatibility.loaders.includes(manifest.tuple.loader) || version.compatibility.server_side === 'unsupported')} onClick={() => void perform(() => createPreview({ action: updateItem ? 'update' : 'install', kind, content_id: updateItem?.id, selection: { provider: project.source, project_id: project.id, version_id: version.id, file_id: artifact.id } }))}>Preview {updateItem ? 'update' : 'installation'} of {artifact.filename}</Button>}</Stack>)}</Stack></Paper>)}</Stack>}
        {admin && <Stack gap="sm"><Title order={4}>Administrator JAR upload</Title><FileInput label="Select a mod or plugin JAR" accept=".jar" value={file} disabled={active} clearable onChange={value => { setFile(value); setUpload(null); setTrial(false); }} /><Button variant="default" disabled={active || !file} onClick={() => void perform(async () => { if (!file) return; if (file.size > 2147483648) throw new Error('JAR exceeds 2 GiB limit.'); const value = await api<Upload>(endpoint + '/uploads?' + new URLSearchParams({ filename: file.name }), { method: 'POST', headers: { 'Content-Type': 'application/octet-stream' }, body: file }); setUpload(value); setNotice('JAR staged without executing or installing it.'); })}>Stage upload</Button>{upload && <><Text size="sm" style={{ overflowWrap: 'anywhere' }}>{upload.filename} · {upload.size.toLocaleString()} bytes · SHA-256 {upload.sha256}</Text>{preview?.manual ? <Button disabled={active} onClick={() => void perform(() => createPreview({ ...(lastMutation ?? { action: 'install', kind }), selection: preview.manual!.selection, upload_id: upload.id, trial: false }))}>Verify upload against publisher digest</Button> : <><Checkbox label={'Trial this unknown upload only against Minecraft ' + manifest.tuple.minecraft + ' / ' + manifest.tuple.loader + ' ' + manifest.tuple.loader_version + '. It remains unknown and blocks automatic upgrades.'} checked={trial} disabled={active} onChange={event => setTrial(event.currentTarget.checked)} /><Button disabled={active || !trial} onClick={() => void perform(() => createPreview({ action: updateItem ? 'update' : 'install', content_id: updateItem?.id, kind, upload_id: upload.id, trial }))}>Preview unknown trial</Button></>}</>}<Button variant="subtle" disabled={active} onClick={() => void perform(async () => { await api(endpoint + '/uploads', { method: 'DELETE' }); setUpload(null); setNotice('Unused actor-owned staging cleared. Previews and recovery references were retained.'); })}>Clear unused upload staging</Button></Stack>}
      </Stack></Paper>}
    </>}
    {preview && <Paper ref={previewRef} tabIndex={-1} withBorder p="md"><Stack gap="sm"><Title order={3}>Review {preview.action.replaceAll('_', ' ')}</Title><Text size="sm">Target: Minecraft {preview.tuple.minecraft} / {preview.tuple.loader} {preview.tuple.loader_version}</Text>{preview.changes.map((change, i) => <Text size="sm" key={i} style={{ overflowWrap: 'anywhere' }}>{change.sha256 ? 'Write ' : 'Remove '}{change.path}</Text>)}{preview.blockers.map((blocker, i) => <Alert key={i} color="orange">{blocker}</Alert>)}{preview.dependencies.length > 0 && <Title order={4}>Dependency plan</Title>}{preview.dependencies.map((dependency, i) => {
      const installed = manifest?.items.filter(item => item.selection?.provider === dependency.provider && item.selection.project_id === dependency.project_id);
      const conflict = ['incompatible', 'conflict', 'conflicts'].includes(dependency.relation);
      const known = ['modrinth', 'curseforge', 'hangar'].includes(dependency.provider) && !!dependency.project_id;
      const cycle = [...parents.map(parent => parent.mutation.selection), lastMutation?.selection].some(selection => selection?.provider === dependency.provider && selection?.project_id === dependency.project_id);
      return <Paper key={i} withBorder p="sm"><Stack gap="xs"><Text size="sm" style={{ overflowWrap: 'anywhere' }}>{dependency.name || dependency.project_id || dependency.version_id || 'Unknown dependency'} · {dependency.relation}{dependency.required ? ' (required)' : ' (optional or informational)'}</Text><Text size="xs" style={{ overflowWrap: 'anywhere' }}>Publisher: {dependency.provider || 'unknown'} · project: {dependency.project_id || 'unknown'} · release: {dependency.version_id || 'choose an exact compatible release'}</Text>{installed?.map(item => <Text size="sm" key={item.id}>Installed identity: {item.name} · {item.selection?.version_id} · {item.enabled ? 'enabled' : 'disabled'}. The preview decides whether it satisfies requirements.</Text>)}{!conflict && !cycle && known && installed?.filter(item => !item.enabled && (!dependency.version_id || item.selection?.version_id === dependency.version_id)).map(item => canOperate && <Button key={item.id} variant="light" disabled={active} onClick={() => void enableDependency(item)}>Preview enabling installed dependency {item.name}</Button>)}{conflict ? <Text c="orange" size="sm">Incompatible dependency: do not install it to satisfy this requirement. Resolve the conflict and re-preview.</Text> : cycle ? <Text c="orange" size="sm">Dependency cycle detected. Resolve publisher metadata before continuing.</Text> : !known ? <Text c="orange" size="sm">Publisher project identity is missing. Obtain the exact project and release from the publisher; names and filenames cannot resolve this dependency. The parent remains blocked until a fresh preview passes.</Text> : <>{!dependency.required && <Text size="sm">Optional dependency: install only if you explicitly want it.</Text>}{canOperate && <Button variant="light" disabled={active} onClick={() => void dependencyPlan(dependency)}>Review dependency {dependency.name || dependency.project_id}</Button>}</>}</Stack></Paper>;
    })}{preview.manual && <Stack gap={4}><Anchor href={preview.manual.official_url} target="_blank" rel="noopener noreferrer">Official manual download</Anchor>{Object.entries(preview.manual.hashes).map(([algorithm, value]) => <Text size="xs" key={algorithm} style={{ overflowWrap: 'anywhere' }}>{algorithm}: {value}</Text>)}<Text size="sm">An administrator can stage the published file above and verify its digest to continue.</Text></Stack>}<Group wrap="wrap">{canOperate && <Button disabled={active || !!preview.blockers.length || !!preview.manual} onClick={() => void apply()}>Apply while stopped</Button>}<Button variant="default" disabled={active} onClick={() => void perform(async () => { await api(endpoint + '/previews/' + preview.id, { method: 'DELETE' }); setPreview(null); await refresh(); })}>Discard preview</Button></Group></Stack></Paper>}
    <Paper withBorder p="md"><Stack gap="md"><Title order={3}>Addon configuration</Title><Text size="sm" c="dimmed">Allowlisted text files in config, defaultconfigs and plugin data folders. Server properties use the server settings screen. Maximum 1 MiB per file.</Text>{listing?.truncated && <Alert color="yellow">Listing reached its 1000-entry limit. Additional files are not shown.</Alert>}{!!listing?.unsupported && <Text size="sm">{listing.unsupported} unsupported or unsafe entries were omitted.</Text>}{listing && !listing.files.length && <Text>No editable addon text files found. Addons may create settings after their first launch.</Text>}{listing?.files.map(item => <Group key={item.path} justify="space-between" wrap="wrap"><Text size="sm" style={{ overflowWrap: 'anywhere', minWidth: 0 }}>{item.path} · {item.size.toLocaleString()} bytes</Text>{canOperate && <Button variant="subtle" disabled={active} onClick={() => void openConfig(item.path)}>Edit {item.path}</Button>}</Group>)}{!canOperate && <Text size="sm">Configuration contents require operator access.</Text>}{editor && canOperate && <Stack gap="sm"><Textarea label={'Edit ' + editor.path} description="Whole-file UTF-8 text. Preserve unrelated keys and comments; another change will require a fresh read." value={text} disabled={active} minRows={12} autosize maxRows={30} onChange={event => setText(event.currentTarget.value)} styles={{ input: { fontFamily: 'monospace', overflowWrap: 'anywhere' } }} /><Group wrap="wrap"><Button disabled={active || text === editor.text} onClick={() => void perform(() => createPreview({ action: 'config_write', config_path: editor.path, expected_sha256: editor.sha256, text }))}>Preview configuration save</Button><Button variant="default" disabled={active} onClick={() => void openConfig(editor.path)}>Reload saved text</Button><Button variant="subtle" disabled={active} onClick={() => { setEditor(null); setText(''); }}>Close editor</Button></Group></Stack>}</Stack></Paper>
  </Stack>;
}

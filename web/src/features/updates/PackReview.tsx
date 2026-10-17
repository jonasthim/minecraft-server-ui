import { Alert, Anchor, Badge, Button, Checkbox, FileInput, Group, Paper, Select, Stack, Text, Title } from '@mantine/core';
import type { components } from '../../api/schema';
import { editPackSelection } from './draft';

type Preview = components['schemas']['ModpackPreview'];
type Request = components['schemas']['ModpackRequest'];
type Props = {
  preview: Preview;
  request: Request;
  editable: boolean;
  administrator: boolean;
  busy: boolean;
  uploadDisabled: boolean;
  archiveFile: File | null;
  memberFiles: Record<string, File | null>;
  onRequest: (next: Request) => void;
  onArchiveFile: (file: File | null) => void;
  onMemberFile: (path: string, file: File | null) => void;
  onUploadArchive: () => void;
  onUploadMember: (path: string) => void;
};
const wrap = { overflowWrap: 'anywhere' as const, minWidth: 0 };
function Hashes({ values }: { values: Record<string, string> }) {
  return <Stack gap={4}>{Object.entries(values).map(([name, hash]) => <Text key={name} size="sm" style={wrap}>{name.toUpperCase()}: {hash}</Text>)}</Stack>;
}
export function PackReview({ preview, request, editable, administrator, busy, uploadDisabled, archiveFile, memberFiles, onRequest, onArchiveFile, onMemberFile, onUploadArchive, onUploadMember }: Props) {
  const disabled = busy || !editable;
  return <Stack gap="md" style={wrap}>
    <Title order={4}>Target pack changes</Title>
    <Anchor href={preview.source.official_url} target="_blank" rel="noopener noreferrer" style={wrap}>Original publisher: {preview.source.name}</Anchor>
    <Text size="sm">Target: Minecraft {preview.tuple.minecraft}, {preview.tuple.loader} {preview.tuple.loader_version}. {preview.summary}</Text>
    <Text size="sm" style={wrap}>Pack SHA256: {preview.source.sha256 || 'Not yet verified'}</Text>
    {!editable && <Text size="sm" c="dimmed">These are the stored pack decisions. Choose manual update mode and an exact pack release to change them in a new preview.</Text>}
    {preview.blockers.map((blocker, index) => <Alert key={index} color="orange">{blocker}</Alert>)}
    {preview.archive_manual && <Paper withBorder p="md"><Stack gap="sm">
      <Title order={5}>Required official pack archive</Title>
      <Text size="sm" style={wrap}>{preview.archive_manual.filename} · {preview.archive_manual.size} bytes</Text>
      <Text size="sm">{preview.archive_manual.reason}</Text>
      <Anchor href={preview.archive_manual.official_url} target="_blank" rel="noopener noreferrer" style={wrap}>Download the exact official archive</Anchor>
      <Hashes values={preview.archive_manual.hashes} />
      {administrator && editable ? <>
        <FileInput label="Exact official pack archive" description="Verification creates a new preview; it does not apply the update." value={archiveFile} onChange={onArchiveFile} disabled={busy || uploadDisabled} clearable />
        {archiveFile && <Text size="sm" style={wrap}>{archiveFile.name}</Text>}
        <Button className="app-wrap-label" disabled={busy || uploadDisabled || !archiveFile} onClick={onUploadArchive}>Verify archive and prepare update preview</Button>
      </> : <Text size="sm">An administrator must verify this exact archive before the update can proceed.</Text>}
    </Stack></Paper>}
    {!!Object.keys(preview.selections).length && <Title order={5}>Server item choices</Title>}
    {Object.entries(preview.selections).map(([path, current]) => {
      const value = { ...current, ...request.selections[path] };
      return <Paper key={path} withBorder p="md"><Stack gap="sm">
        <Text fw={600} style={wrap}>{path}</Text>
        {!current.optional && <Badge w="fit-content" variant="light">Required server item</Badge>}
        {current.optional && <Checkbox label={'Include optional server item ' + path} checked={value.selected} disabled={disabled} onChange={event => onRequest(editPackSelection(request, preview, path, { selected: event.currentTarget.checked }))} />}
        <Checkbox label={'Keep disabled: ' + path} checked={value.disabled} disabled={disabled} onChange={event => onRequest(editPackSelection(request, preview, path, { disabled: event.currentTarget.checked }))} />
        <Checkbox label={'Pin local item: ' + path} checked={value.pinned} disabled={disabled} onChange={event => onRequest(editPackSelection(request, preview, path, { pinned: event.currentTarget.checked }))} />
        <Checkbox label={'Exclude upstream item: ' + path} checked={value.excluded} disabled={disabled} onChange={event => onRequest(editPackSelection(request, preview, path, { excluded: event.currentTarget.checked }))} />
      </Stack></Paper>;
    })}
    {!!preview.changes.length && <Title order={5}>Paths and conflicts</Title>}
    {preview.changes.map(change => {
      const choice = request.choices[change.path] ?? (change.choice === 'keep' || change.choice === 'upstream' ? change.choice : null);
      return <Paper key={change.path} withBorder p="md"><Stack gap="sm">
        <Group wrap="wrap"><Badge color={change.action === 'conflict' ? 'orange' : 'blue'}>{change.action}</Badge><Text fw={600} style={wrap}>{change.path}</Text></Group>
        <Text size="sm">{change.reason}</Text>
        {(change.action === 'conflict' || choice) && <Select label={'Resolve ' + change.path} value={choice} allowDeselect={false} disabled={disabled} data={[{ value: 'keep', label: 'Preserve local file or deliberate deletion' }, { value: 'upstream', label: change.upstream ? 'Use exact upstream file' : 'Delete file removed by publisher' }]} onChange={value => { if (value === 'keep' || value === 'upstream') onRequest({ ...request, choices: { ...request.choices, [change.path]: value } }); }} />}
        <details><summary>File comparison hashes</summary><Stack mt="xs" gap={4}>{[['Previous baseline', change.baseline], ['Current local', change.before], ['Proposed upstream', change.upstream]].map(([label, value]) => <Text key={label} size="sm" style={wrap}>{label}: {value || 'Absent'}</Text>)}</Stack></details>
      </Stack></Paper>;
    })}
    {preview.manual.map(item => <Paper key={item.path} withBorder p="md"><Stack gap="sm">
      <Title order={5} style={wrap}>Required publisher file: {item.path}</Title>
      <Text size="sm">{item.reason}</Text>
      <Anchor href={item.official_url} target="_blank" rel="noopener noreferrer" style={wrap}>Download the exact publisher file</Anchor>
      <Hashes values={item.hashes} />
      {administrator && editable ? <>
        <FileInput label={'Exact publisher file for ' + item.path} value={memberFiles[item.path] ?? null} onChange={file => onMemberFile(item.path, file)} disabled={busy || uploadDisabled} clearable />
        {memberFiles[item.path] && <Text size="sm" style={wrap}>{memberFiles[item.path]?.name}</Text>}
        <Button className="app-wrap-label" disabled={busy || uploadDisabled || !memberFiles[item.path]} onClick={() => onUploadMember(item.path)}>Verify file and prepare update preview</Button>
      </> : <Text size="sm">An administrator must verify this file against the publisher’s hashes before the update can proceed.</Text>}
    </Stack></Paper>)}
    {uploadDisabled && editable && <Text size="sm" c="dimmed">Prepare the changed choices again before uploading files. Uploads must match the exact current, unexpired preview.</Text>}
    {!!preview.excluded.length && <Text size="sm" style={wrap}>Excluded entries: {preview.excluded.join(', ')}</Text>}
    <Text size="sm" c="dimmed">All decisions require a new verified update preview. Uploading, preparing or refreshing never applies an update or rewinds world data.</Text>
  </Stack>;
}

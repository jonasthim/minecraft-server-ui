import { Anchor, Button, FileInput, Paper, Stack, Text, Title } from '@mantine/core';
import type { components } from '../../api/schema';

type Props = {
  required: NonNullable<components['schemas']['UpdatePlan']['content_manual_required']>;
  files: Record<string, File | null>;
  administrator: boolean;
  editable: boolean;
  busy: boolean;
  uploadDisabled: boolean;
  onFile: (id: string, file: File | null) => void;
  onUpload: (id: string) => void;
};
export function ContentManualReview({ required, files, administrator, editable, busy, uploadDisabled, onFile, onUpload }: Props) {
  return <Stack gap="md">
    <Title order={4}>Required content publisher files</Title>
    {Object.entries(required).map(([id, item]) => <Paper key={id} withBorder p="md"><Stack gap="sm" style={{ minWidth: 0, overflowWrap: 'anywhere' }}>
      <Title order={5}>{item.filename}</Title>
      <Text size="sm">{item.reason}</Text>
      <Text size="sm">Publisher: {item.selection.provider} · Project: {item.selection.project_id} · Release: {item.selection.version_id} · File: {item.selection.file_id}</Text>
      <Anchor href={item.official_url} target="_blank" rel="noopener noreferrer">Download the exact content publisher file</Anchor>
      {Object.entries(item.hashes).map(([name, hash]) => <Text key={name} size="sm">{name.toUpperCase()}: {hash}</Text>)}
      {administrator && editable ? <>
        <FileInput label={'Exact content publisher file for ' + item.filename} description="The upload is staged for verification. A new preview checks its publisher hashes and target compatibility." value={files[id] ?? null} onChange={file => onFile(id, file)} disabled={busy || uploadDisabled} clearable accept=".jar" />
        {files[id] && <Text size="sm">{files[id]?.name}</Text>}
        <Button className="app-wrap-label" disabled={busy || uploadDisabled || !files[id]} onClick={() => onUpload(id)}>Stage content file and prepare update preview</Button>
      </> : <Text size="sm">An administrator must verify this exact file before the update can proceed.</Text>}
      {uploadDisabled && editable && <Text size="sm" c="dimmed">Prepare any changed choices again before uploading a file for this preview.</Text>}
    </Stack></Paper>)}
  </Stack>;
}

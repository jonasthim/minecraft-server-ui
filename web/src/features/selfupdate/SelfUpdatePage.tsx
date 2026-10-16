import { useEffect, useState } from 'react';
import { Alert, Badge, Button, Checkbox, Code, Group, Paper, Stack, Text, TextInput, Title } from '@mantine/core';
import { api } from '../../api/client';
import type { components } from '../../api/schema';

type Status = components['schemas']['SelfUpdateStatus'];
type Check = components['schemas']['SelfUpdateCheck'];
type Stage = components['schemas']['SelfUpdateStage'];
const message = (error: unknown) => error instanceof Error ? error.message : 'Manager update request failed.';
export function SelfUpdatePage() {
  const [status, setStatus] = useState<Status | null>(null);
  const [version, setVersion] = useState('');
  const [checked, setChecked] = useState<Check | null>(null);
  const [stage, setStage] = useState<Stage | null>(null);
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [refresh, setRefresh] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    void api<Status>('/system/update', { signal: controller.signal }).then(value => { if (!controller.signal.aborted) { setStatus(value); setError(''); } }).catch(error => { if (!controller.signal.aborted) setError(message(error)); });
    return () => controller.abort();
  }, [refresh]);
  const run = async (operation: 'check' | 'stage' | 'apply') => {
    setBusy(true); setError(''); setNotice('');
    try {
      if (operation === 'check') { setChecked(null); setStage(null); setConfirm(false); setChecked(await api<Check>('/system/update/check', { method: 'POST', body: JSON.stringify({ version } satisfies components['schemas']['SelfUpdateVersionInput']) })); setNotice('Release signature, platform and schema verified.'); }
      if (operation === 'stage') { setStage(await api<Stage>('/system/update/stage', { method: 'POST', body: JSON.stringify({ version } satisfies components['schemas']['SelfUpdateVersionInput']) })); setNotice('Release downloaded and verified. It is staged for native verification.'); }
      if (operation === 'apply' && stage) { await api<void>('/system/update/apply', { method: 'POST', body: JSON.stringify({ stage_id: stage.id, confirm } satisfies components['schemas']['SelfUpdateApplyInput']) }); setNotice('Native activation requested. The manager may disconnect while it restarts. Refresh after it returns to check the installed version.'); setStage(null); setConfirm(false); }
    } catch (error) { setError(message(error)); }
    finally { setBusy(false); }
  };
  return <Stack gap="lg">
    <Group justify="space-between" wrap="wrap"><Title order={1}>Manager updates</Title><Button variant="light" disabled={busy} onClick={() => setRefresh(value => value + 1)}>Refresh status</Button></Group>
    {error && <Alert color="red" role="alert">{error}</Alert>}
    {notice && <Alert color="blue" role="status">{notice}</Alert>}
    <Paper withBorder p="lg"><Stack gap="sm"><Text>Installed version: <Code>{status?.current_version || 'Checking…'}</Code></Text><Badge color={status?.activation_available ? 'green' : 'yellow'}>{status?.activation_available ? 'Native updater ready' : 'Activation unavailable'}</Badge>
      {status?.disabled_reason && <Alert color="yellow">{status.disabled_reason}</Alert>}
      <Text size="sm">Only signed stable releases from the configured publisher are accepted. Updates that change the manager database schema require manual maintenance.</Text>
      <Text size="sm">An unsuccessful activation restores the prior manager executable. Server worlds and backups are preserved. Linux servers stay running; Windows servers stop gracefully and configured autostart resumes them.</Text>
    </Stack></Paper>
    <Paper withBorder p="lg"><Stack><Title order={2}>Choose a stable release</Title>
      <TextInput label="Release version" placeholder="v1.2.3" value={version} disabled={busy} maxLength={64} onChange={event => { setVersion(event.currentTarget.value); setChecked(null); setStage(null); setConfirm(false); }} />
      <Button disabled={busy || !status?.trust_configured || !/^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/.test(version)} loading={busy} onClick={() => void run('check')}>Verify release metadata</Button>
      {checked && <><Text size="sm">Verified {checked.version} for {checked.platform}. Valid until {new Date(checked.expires_at).toLocaleString()}.</Text><Text size="xs" style={{ overflowWrap: 'anywhere' }}>Executable SHA-256: {checked.binary_sha256}</Text><Button variant="light" disabled={busy || !!stage} onClick={() => void run('stage')}>Download and verify release</Button></>}
      {stage && <><Alert color="blue">{stage.version} is staged. Activation verifies the release again inside the native updater.</Alert><Checkbox label="I confirm restarting the manager and the Windows server shutdown described above." checked={confirm} disabled={busy || !status?.activation_available} onChange={event => setConfirm(event.currentTarget.checked)} /><Button color="orange" disabled={busy || !confirm || !status?.activation_available} onClick={() => void run('apply')}>Activate manager update</Button></>}
    </Stack></Paper>
  </Stack>;
}

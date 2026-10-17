import { useEffect, useRef, useState } from 'react';
import { Alert, Badge, Button, Checkbox, Code, Group, Paper, Select, Stack, Text, TextInput, Title } from '@mantine/core';
import { api, APIError } from '../../api/client';
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
  const [statusError, setStatusError] = useState('');
  const pendingStageRecovery = useRef<Check | null>(null);
  const [notice, setNotice] = useState('');
  const [refresh, setRefresh] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    void api<Status>('/system/update', { signal: controller.signal }).then(value => {
      if (controller.signal.aborted) return;
      setStatus(value); setStatusError('');
      setStage(previous => value.stages.find(item => item.id === previous?.id) ?? value.stages[0] ?? null);
      setConfirm(false);
      const expected = pendingStageRecovery.current;
      if (expected && value.stages.some(item => item.version === expected.version && item.platform === expected.platform && item.schema === expected.schema && item.binary_sha256 === expected.binary_sha256)) {
        pendingStageRecovery.current = null;
        setError('');
        setNotice(`The staging response was lost, but ${expected.version} was recovered from verified staged status. Activation still requires confirmation.`);
      }
    }).catch(error => { if (!controller.signal.aborted) setStatusError(message(error)); });
    return () => controller.abort();
  }, [refresh]);
  const run = async (operation: 'check' | 'stage' | 'apply') => {
    setBusy(true); setError(''); setNotice(''); pendingStageRecovery.current = null;
    try {
      if (operation === 'check') { setConfirm(false); setChecked(null); setChecked(await api<Check>('/system/update/check', { method: 'POST', body: JSON.stringify({ version } satisfies components['schemas']['SelfUpdateVersionInput']) })); setNotice('Release signature, platform and schema verified.'); }
      if (operation === 'stage') { setStage(await api<Stage>('/system/update/stage', { method: 'POST', body: JSON.stringify({ version } satisfies components['schemas']['SelfUpdateVersionInput']) })); setNotice('Release downloaded and verified. It is staged for native verification.'); }
      if (operation === 'apply' && stage) { await api<void>('/system/update/apply', { method: 'POST', body: JSON.stringify({ stage_id: stage.id, confirm } satisfies components['schemas']['SelfUpdateApplyInput']) }); setNotice('Native activation requested. The manager may disconnect while it restarts. Refresh after it returns to check the installed version.'); setStage(null); setConfirm(false); }
    } catch (error) { setError(message(error)); if (operation === 'stage' && !(error instanceof APIError)) pendingStageRecovery.current = checked; }
    finally { setBusy(false); if (operation !== 'check') setRefresh(value => value + 1); }
  };
  return <Stack gap="lg">
    <Group justify="space-between" wrap="wrap"><Title order={1}>Manager updates</Title><Button variant="light" disabled={busy} onClick={() => setRefresh(value => value + 1)}>Refresh status</Button></Group>
    {error && <Alert color="red" role="alert">{error}</Alert>}
    {statusError && <Alert color="red" role="alert">{statusError}</Alert>}
    {notice && <Alert color="blue" role="status">{notice}</Alert>}
    <Paper withBorder p="lg"><Stack gap="sm"><Text>Installed version: <Code>{status?.current_version || 'Checking…'}</Code></Text><Badge color={status?.activation_available ? 'green' : 'yellow'}>{status?.activation_available ? 'Native updater ready' : 'Activation unavailable'}</Badge>
      {status?.disabled_reason && <Alert color="yellow">{status.disabled_reason}</Alert>}
      <Text size="sm">Only signed stable releases from the configured publisher are accepted. Updates that change the manager database schema require manual maintenance.</Text>
      <Text size="sm">An unsuccessful activation restores the prior manager executable. Server worlds and backups are preserved. Linux servers stay running; Windows servers stop gracefully and configured autostart resumes them.</Text>
    </Stack></Paper>
    <Paper withBorder p="lg"><Stack><Title order={2}>Choose a stable release</Title>
      <TextInput label="Release version" placeholder="v1.2.3" value={version} disabled={busy} maxLength={64} onChange={event => { setVersion(event.currentTarget.value); setChecked(null); setConfirm(false); }} />
      <Button disabled={busy || !status?.trust_configured || !/^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/.test(version)} loading={busy} onClick={() => void run('check')}>Verify release metadata</Button>
      {checked && <><Text size="sm">Verified {checked.version} for {checked.platform}. Valid until {new Date(checked.expires_at).toLocaleString()}.</Text><Text size="xs" style={{ overflowWrap: 'anywhere' }}>Executable SHA-256: {checked.binary_sha256}</Text><Button variant="light" disabled={busy} onClick={() => void run('stage')}>Download and verify release</Button></>}
    </Stack></Paper>
    <Paper withBorder p="lg"><Stack><Title order={2}>Staged releases</Title>
      {status?.staging_error && <Alert color="yellow">{status.staging_error}</Alert>}
      {status && status.stages.length === 0 && !status.staging_error && <Text size="sm">No verified release is staged.</Text>}
      {!!status?.stages.length && <Select label="Verified staged release" value={stage?.id ?? null} data={status.stages.map(item => ({ value: item.id, label: `${item.version} · ${item.platform}` }))} disabled={busy} allowDeselect={false} onChange={id => { setStage(status.stages.find(item => item.id === id) ?? null); setConfirm(false); }} />}
      {stage && <><Alert color="blue">{stage.version} is staged. Activation verifies the release again inside the native updater.</Alert><Checkbox label="I confirm restarting the manager and the Windows server shutdown described above." checked={confirm} disabled={busy || !status?.activation_available} onChange={event => setConfirm(event.currentTarget.checked)} /><Button color="orange" disabled={busy || !confirm || !status?.activation_available} onClick={() => void run('apply')}>Activate manager update</Button></>}
    </Stack></Paper>
  </Stack>;
}

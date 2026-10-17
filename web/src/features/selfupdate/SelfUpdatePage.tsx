import { useEffect, useRef, useState } from 'react';
import { Alert, Anchor, Badge, Button, Checkbox, Code, Group, Paper, Select, Stack, Text, TextInput, Title } from '@mantine/core';
import { api, APIError } from '../../api/client';
import type { components } from '../../api/schema';
import { useActivationReconnect } from './useActivationReconnect';

type Status = components['schemas']['SelfUpdateStatus'];
type Check = components['schemas']['SelfUpdateCheck'];
type Stage = components['schemas']['SelfUpdateStage'];
type Discovery = components['schemas']['SelfUpdateDiscovery'];
const message = (error: unknown) => error instanceof Error ? error.message : 'Manager update request failed.';
const comparisonText: Record<Discovery['comparison'], string> = {
  newer: 'A newer stable release is available.',
  same: 'The publisher release matches the installed stable version.',
  older: 'The publisher release is older than the installed stable version.',
  unknown: 'The installed version cannot be compared with a stable release.',
  no_release: 'No published stable release was found for the configured publisher.',
};

export function SelfUpdatePage() {
  const [status, setStatus] = useState<Status | null>(null);
  const [version, setVersion] = useState('');
  const [checked, setChecked] = useState<Check | null>(null);
  const [stage, setStage] = useState<Stage | null>(null);
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [statusError, setStatusError] = useState('');
  const [discovery, setDiscovery] = useState<Discovery | null>(null);
  const [discoveryError, setDiscoveryError] = useState('');
  const [discovering, setDiscovering] = useState(false);
  const discoveryRequest = useRef<AbortController | null>(null);
  const operationRequest = useRef<AbortController | null>(null);
  const pendingStageRecovery = useRef<Check | null>(null);
  const [notice, setNotice] = useState('');
  const [refresh, setRefresh] = useState(0);
  const reconnect = useActivationReconnect(setStatus);
  const locked = busy || reconnect.waiting;

  useEffect(() => () => { discoveryRequest.current?.abort(); operationRequest.current?.abort(); }, []);
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

  const discover = async () => {
    discoveryRequest.current?.abort();
    const controller = new AbortController(); discoveryRequest.current = controller;
    setDiscovering(true); setDiscoveryError(''); setDiscovery(null);
    try {
      const value = await api<Discovery>('/system/update/discover', { method: 'POST', signal: controller.signal });
      if (!controller.signal.aborted) setDiscovery(value);
    } catch (error) {
      if (!controller.signal.aborted) setDiscoveryError('Latest release check failed: ' + message(error));
    } finally { if (!controller.signal.aborted) setDiscovering(false); }
  };
  const selectVersion = (value: string) => {
    setVersion(value); setChecked(null); setStage(null); setConfirm(false);
    setNotice(''); pendingStageRecovery.current = null;
  };
  const run = async (operation: 'check' | 'stage' | 'apply') => {
    operationRequest.current?.abort();
    const controller = new AbortController(); operationRequest.current = controller;
    setBusy(true); setError(''); setNotice(''); pendingStageRecovery.current = null;
    const selected = stage;
    try {
      if (operation === 'check') {
        setConfirm(false); setChecked(null);
        const value = await api<Check>('/system/update/check', { method: 'POST', signal: controller.signal, body: JSON.stringify({ version } satisfies components['schemas']['SelfUpdateVersionInput']) });
        if (controller.signal.aborted) return;
        setChecked(value); setNotice('Release signature, platform and schema verified.');
      }
      if (operation === 'stage') {
        setConfirm(false);
        const value = await api<Stage>('/system/update/stage', { method: 'POST', signal: controller.signal, body: JSON.stringify({ version } satisfies components['schemas']['SelfUpdateVersionInput']) });
        if (controller.signal.aborted) return;
        setStage(value); setNotice('Release downloaded and verified. It is staged for native verification.');
      }
      if (operation === 'apply' && selected) {
        setConfirm(false);
        const request = new AbortController();
        const abort = () => request.abort();
        controller.signal.addEventListener('abort', abort, { once: true });
        const timeout = setTimeout(abort, 10000);
        try {
          await api<void>('/system/update/apply', { method: 'POST', signal: request.signal, body: JSON.stringify({ stage_id: selected.id, confirm } satisfies components['schemas']['SelfUpdateApplyInput']) });
        } finally {
          clearTimeout(timeout); controller.signal.removeEventListener('abort', abort);
        }
        if (controller.signal.aborted) return;
        setStage(null); reconnect.start(selected.version, false);
      }
    } catch (error) {
      if (controller.signal.aborted) return;
      // The shared client uses request_failed when a gateway returns HTML or
      // another generic response. That cannot establish application rejection.
      const ambiguousActivation = !(error instanceof APIError) || !error.code || error.code === 'request_failed';
      if (operation === 'apply' && selected && ambiguousActivation) {
        setStage(null); reconnect.start(selected.version, true);
      } else {
        setError(message(error));
        if (operation === 'stage' && !(error instanceof APIError)) pendingStageRecovery.current = checked;
      }
    } finally {
      if (!controller.signal.aborted) {
        setBusy(false);
        if (operation === 'stage') setRefresh(value => value + 1);
      }
    }
  };

  return <Stack gap="lg">
    <Group justify="space-between" wrap="wrap"><Title order={1}>Manager updates</Title><Button variant="light" disabled={locked} onClick={() => setRefresh(value => value + 1)}>Refresh status</Button></Group>
    {error && <Alert color="red" role="alert">{error}</Alert>}
    {statusError && <Alert color="red" role="alert">{statusError}</Alert>}
    {notice && <Alert color="blue" role="status">{notice}</Alert>}
    {reconnect.message && <Alert color={reconnect.failed ? 'yellow' : 'blue'} role={reconnect.failed ? 'alert' : 'status'}><Stack gap="sm"><Text size="sm">{reconnect.message}</Text>{reconnect.waiting && <Button variant="light" onClick={reconnect.cancel}>Stop waiting</Button>}</Stack></Alert>}
    <Paper withBorder p="lg"><Stack gap="sm"><Text>Installed version: <Code>{status?.current_version || 'Checking…'}</Code></Text><Badge color={status?.activation_available ? 'green' : 'yellow'}>{status?.activation_available ? 'Native updater ready' : 'Activation unavailable'}</Badge>
      {status?.disabled_reason && <Alert color="yellow">{status.disabled_reason}</Alert>}
      <Text size="sm">Only signed stable releases from the configured publisher are accepted. Updates that change the manager database schema require manual maintenance.</Text>
      <Text size="sm">An unsuccessful activation restores the prior manager executable. Server worlds and backups are preserved. Linux servers stay running; Windows servers stop gracefully and configured autostart resumes them.</Text>
    </Stack></Paper>
    <Paper withBorder p="lg"><Stack><Title order={2}>Latest publisher release</Title>
      <Text size="sm">Check the configured publisher's stable release listing. Listing details and release notes are unsigned; selecting a version still requires signed metadata verification before download. Discovery never stages or activates an update.</Text>
      <Button disabled={locked || discovering || !status?.trust_configured} loading={discovering} onClick={() => void discover()}>{discoveryError ? 'Retry latest release check' : 'Check latest stable release'}</Button>
      {discovering && <Button variant="subtle" onClick={() => { discoveryRequest.current?.abort(); setDiscovering(false); }}>Cancel release check</Button>}
      {discoveryError && <Alert color="red" role="alert">{discoveryError}</Alert>}
      {discovery && <Stack gap="sm">
        <Text role="status">{comparisonText[discovery.comparison]}</Text>
        <Text size="sm" c="dimmed">Checked {new Date(discovery.checked_at).toLocaleString()} · {discovery.cached ? 'Cached publisher observation (up to five minutes)' : 'Fresh publisher observation'} · Installed at check: {discovery.current_version}</Text>
        {discovery.release && <>
          <Text fw={600} style={{ overflowWrap: 'anywhere' }}>{discovery.release.title}</Text>
          <Text size="sm">{discovery.release.version} · Published {new Date(discovery.release.published_at).toLocaleString()}</Text>
          <Text size="sm" role="region" aria-label="Publisher release notes" tabIndex={0} style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', maxHeight: 320, overflowY: 'auto' }}>{discovery.release.notes || 'No release notes were supplied.'}</Text>
          {discovery.release.notes_truncated && <Text size="sm" c="dimmed">Release notes were truncated.</Text>}
          <Anchor href={discovery.release.url} target="_blank" rel="noopener noreferrer">View publisher release</Anchor>
          <Button variant="light" disabled={locked} onClick={() => selectVersion(discovery.release!.version)}>Use this version</Button>
        </>}
      </Stack>}
    </Stack></Paper>
    <Paper withBorder p="lg"><Stack><Title order={2}>Choose a stable release</Title>
      <TextInput label="Release version" placeholder="v1.2.3" value={version} disabled={locked} maxLength={64} onChange={event => selectVersion(event.currentTarget.value)} />
      <Button disabled={locked || !status?.trust_configured || !/^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/.test(version)} loading={busy} onClick={() => void run('check')}>Verify release metadata</Button>
      {checked && <><Text size="sm">Verified {checked.version} for {checked.platform}. Valid until {new Date(checked.expires_at).toLocaleString()}.</Text><Text size="xs" style={{ overflowWrap: 'anywhere' }}>Executable SHA-256: {checked.binary_sha256}</Text><Button variant="light" disabled={locked} onClick={() => void run('stage')}>Download and verify release</Button></>}
    </Stack></Paper>
    <Paper withBorder p="lg"><Stack><Title order={2}>Staged releases</Title>
      {status?.staging_error && <Alert color="yellow">{status.staging_error}</Alert>}
      {status && status.stages.length === 0 && !status.staging_error && <Text size="sm">No verified release is staged.</Text>}
      {!!status?.stages.length && <Select label="Verified staged release" value={stage?.id ?? null} data={status.stages.map(item => ({ value: item.id, label: `${item.version} · ${item.platform}` }))} disabled={locked} allowDeselect={false} onChange={id => { setStage(status.stages.find(item => item.id === id) ?? null); setConfirm(false); }} />}
      {stage && <><Alert color="blue">{stage.version} is staged. Activation verifies the release again inside the native updater.</Alert><Checkbox label="I confirm restarting the manager and the Windows server shutdown described above." checked={confirm} disabled={locked || !status?.activation_available} onChange={event => setConfirm(event.currentTarget.checked)} /><Button color="orange" disabled={locked || !confirm || !status?.activation_available} onClick={() => void run('apply')}>Activate manager update</Button></>}
    </Stack></Paper>
  </Stack>;
}

import { useCallback, useEffect, useRef, useState } from 'react';
import { Alert, Button, Group, Loader, Paper, Stack, Text, TextInput, Title } from '@mantine/core';
import { api, APIError } from '../../api/client';
import type { components } from '../../api/schema';
import { useAuth } from '../auth/AuthProvider';

type Policy = components['schemas']['BackupRetentionPolicy'];
type Input = components['schemas']['BackupRetentionPolicyInput'];
const message = (error: unknown) => error instanceof Error ? error.message : 'Could not load or save retention policy.';
const validNumber = (value: string, maximum: number) => /^\d+$/.test(value) && Number.isSafeInteger(Number(value)) && Number(value) <= maximum;

export function RetentionPolicyPanel({ instanceId }: { instanceId: string }) {
  const { user } = useAuth();
  return <RetentionEditor key={instanceId + ':' + user?.id + ':' + user?.role} instanceId={instanceId} canEdit={user?.role === 'admin'} />;
}

function RetentionEditor({ instanceId, canEdit }: { instanceId: string; canEdit: boolean }) {
  const endpoint = '/instances/' + encodeURIComponent(instanceId) + '/backups/retention';
  const [policy, setPolicy] = useState<Policy | null>(null);
  const [count, setCount] = useState('');
  const [days, setDays] = useState('');
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [stale, setStale] = useState(false);
  const scope = useRef<AbortController | null>(null);
  const load = useCallback(async (signal: AbortSignal, preserveEdits: boolean) => {
    setLoading(true); setError(''); setNotice('');
    try {
      const current = await api<Policy>(endpoint, { signal });
      if (signal.aborted) return;
      setPolicy(current); setStale(false);
      if (!preserveEdits) { setCount(String(current.keep_last)); setDays(String(current.keep_days)); }
      else setNotice('Current policy reloaded. Your edited values are preserved. Review them before saving.');
    } catch (error) { if (!signal.aborted) setError(message(error)); }
    finally { if (!signal.aborted) setLoading(false); }
  }, [endpoint]);
  useEffect(() => {
    const controller = new AbortController(); scope.current = controller;
    void load(controller.signal, false);
    return () => { controller.abort(); scope.current = null; };
  }, [load]);
  const save = async () => {
    const signal = scope.current?.signal;
    if (!policy || !canEdit || !signal || signal.aborted || stale || !validNumber(count, 10000) || !validNumber(days, 36500)) return;
    setBusy(true); setError(''); setNotice('');
    const input: Input = { keep_last: Number(count), keep_days: Number(days), expected_revision: policy.revision };
    try {
      const current = await api<Policy>(endpoint, { method: 'PUT', body: JSON.stringify(input), signal });
      if (signal.aborted) return;
      setPolicy(current); setCount(String(current.keep_last)); setDays(String(current.keep_days));
      setNotice('Retention policy saved. Pruning runs after the next successful automatic backup.');
    } catch (error) { if (!signal.aborted) { setError(message(error)); if (error instanceof APIError && error.code === 'conflict') setStale(true); } }
    finally { if (!signal.aborted) setBusy(false); }
  };
  const dirty = policy && (Number(count) !== policy.keep_last || Number(days) !== policy.keep_days);
  return <Paper withBorder p="md"><Stack gap="sm">
    <Group justify="space-between" wrap="wrap"><Title order={3}>Automatic backup retention</Title><Button variant="subtle" disabled={busy || loading} onClick={() => { if (scope.current) void load(scope.current.signal, policy !== null); }}>Refresh retention policy</Button></Group>
    <Text size="sm">Defaults: 10 automatic archives and 30 days. 0 disables that limit; both values 0 disable automatic pruning.</Text>
    <Text size="sm">Eligible automatic archives are pruned when either enabled limit is exceeded, after the next successful automatic backup. Saving this policy deletes no archives. Manual and safety backups remain protected; active downloads and restore references are preserved.</Text>
    {loading && <Loader size="sm" aria-label="Loading retention policy" />}
    {error && <Alert color="red" role="alert">{error}</Alert>}
    {notice && <Alert color="green" role="status">{notice}</Alert>}
    {policy && <Text size="sm" fw={600}>Effective policy: {policy.keep_last === 0 ? 'unlimited automatic archives' : policy.keep_last + ' automatic archives'}; {policy.keep_days === 0 ? 'no age limit' : policy.keep_days + ' days'}.</Text>}
    {canEdit && policy && <form onSubmit={event => { event.preventDefault(); void save(); }}><Stack gap="sm">
      <TextInput label="Automatic archives to retain" description="0–10000; 0 disables the count limit." inputMode="numeric" value={count} onChange={event => setCount(event.currentTarget.value)} disabled={busy} error={!validNumber(count, 10000) ? 'Enter a whole number from 0 to 10000.' : undefined} />
      <TextInput label="Maximum age in days" description="0–36500; 0 disables the age limit." inputMode="numeric" value={days} onChange={event => setDays(event.currentTarget.value)} disabled={busy} error={!validNumber(days, 36500) ? 'Enter a whole number from 0 to 36500.' : undefined} />
      {stale && <Button variant="outline" disabled={loading || busy} onClick={() => { if (scope.current) void load(scope.current.signal, true); }}>Reload current policy, keep my edits</Button>}
      {dirty && <Text size="sm" c="dimmed">Unsaved retention values.</Text>}
      <Group wrap="wrap"><Button type="submit" loading={busy} disabled={loading || stale || !dirty || !validNumber(count, 10000) || !validNumber(days, 36500)}>Save retention policy</Button><Button variant="default" disabled={busy} onClick={() => { setCount('10'); setDays('30'); setNotice('Default values selected. Save to apply them.'); }}>Use default values</Button></Group>
    </Stack></form>}
    {!canEdit && <Text size="sm" c="dimmed">An administrator can change this policy.</Text>}
  </Stack></Paper>;
}

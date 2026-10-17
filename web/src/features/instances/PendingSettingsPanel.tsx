import { useCallback, useEffect, useRef, useState } from 'react';
import { Alert, Badge, Button, Group, Loader, Modal, Paper, SimpleGrid, Stack, Text, Title } from '@mantine/core';
import { api, APIError } from '../../api/client';
import type { components } from '../../api/schema';
import { useAuth } from '../auth/AuthProvider';
import { InstanceForm } from './InstancesPage';

type Detail = components['schemas']['InstanceDetail'];
type Pending = components['schemas']['PendingSettings'];
const message = (error: unknown) => error instanceof Error ? error.message : 'Request failed. Try again.';

export function PendingSettingsPanel({ detail, refreshActive, onBlocked }: { detail: Detail; refreshActive: (signal?: AbortSignal) => Promise<void>; onBlocked: (blocked: boolean) => void }) {
  const { user } = useAuth();
  return <PendingScope key={detail.instance.id + ':' + user?.id + ':' + user?.role} detail={detail} refreshActive={refreshActive} onBlocked={onBlocked} />;
}
function PendingScope({ detail, refreshActive, onBlocked }: { detail: Detail; refreshActive: (signal?: AbortSignal) => Promise<void>; onBlocked: (blocked: boolean) => void }) {
  const { user } = useAuth(); const admin = user?.role === 'admin';
  const [state, setState] = useState<Pending | null>(null);
  const [readError, setReadError] = useState(''); const [mutationError, setMutationError] = useState(''); const [notice, setNotice] = useState('');
  const [loading, setLoading] = useState(true); const [discarding, setDiscarding] = useState<{ revision: number; name: string } | null>(null); const [busy, setBusy] = useState(false);
  const [editor, setEditor] = useState<{ detail: Detail; revision: number; base: string; stale: boolean } | null>(null);
  const [conflict, setConflict] = useState(false);
  const lifetime = useRef(new AbortController());
  const generation = useRef(0);
  const pendingRead = useRef<AbortController | null>(null);
  const path = '/instances/' + encodeURIComponent(detail.instance.id) + '/pending-settings';
  const reload = useCallback(async (keepEdits = false, explicit = true) => {
    // Explicit refresh supersedes a poll. Polls never supersede an explicit read.
    if (!explicit && pendingRead.current && !pendingRead.current.signal.aborted) return;
    pendingRead.current?.abort();
    const controller = new AbortController(); pendingRead.current = controller;
    const epoch = ++generation.current;
    const signal = AbortSignal.any([lifetime.current.signal, controller.signal]);
    if (explicit) setLoading(true);
    try {
      // A base reload changes CAS values only after this complete read is still current.
      const [current, applied] = await Promise.all([
        api<Pending>(path, { signal }),
        keepEdits ? api<{ detail: Detail }>('/instances/' + encodeURIComponent(detail.instance.id), { signal }) : Promise.resolve(null),
      ]);
      if (signal.aborted || epoch !== generation.current) return;
      await refreshActive(signal);
      if (signal.aborted || epoch !== generation.current) return;
      setState(current); setReadError('');
      if (keepEdits && applied) {
        setEditor(previous => previous ? { ...previous, revision: current.revision, base: applied.detail.instance.updated_at ?? '', stale: current.stale } : previous);
        setConflict(false);
      }
    } catch (error) {
      if (!signal.aborted && epoch === generation.current) setReadError(message(error));
    } finally {
      if (pendingRead.current === controller) pendingRead.current = null;
      if (!signal.aborted && epoch === generation.current) setLoading(false);
    }
  }, [path, detail.instance.id, refreshActive]);
  useEffect(() => {
    const controller = new AbortController(); lifetime.current = controller;
    void reload(false, false);
    const timer = window.setInterval(() => void reload(false, false), 10000);
    return () => { controller.abort(); pendingRead.current?.abort(); window.clearInterval(timer); };
  }, [reload]);
  useEffect(() => { onBlocked(loading || !!readError || state === null || state.draft !== null); }, [loading, readError, state, onBlocked]);
  useEffect(() => { if (state && !state.draft) setNotice(previous => previous.startsWith('Desired settings saved') ? '' : previous); }, [state]);
  function edit() {
    if (!state || readError) return;
    const desired = state.draft;
    setEditor({ detail: desired ? { ...detail, instance: { ...detail.instance, name: desired.name, memory_mib: desired.memory_mib }, config: desired.config } : detail, revision: state.revision, base: detail.instance.updated_at ?? '', stale: state.stale });
    setConflict(false); setMutationError(''); setNotice('');
  }
  async function discard() {
    if (!discarding) return; setBusy(true); setMutationError(''); setNotice(''); const signal = lifetime.current.signal;
    try {
      const body: components['schemas']['DiscardPendingSettings'] = { expected_revision: discarding.revision };
      const result = await api<Pending>(path, { method: 'DELETE', body: JSON.stringify(body), signal });
      if (signal.aborted) return;
      generation.current++; pendingRead.current?.abort(); setLoading(false); setState(result); setDiscarding(null); setNotice('Saved desired settings discarded. Applied settings were not changed.');
      await refreshActive(signal);
    } catch (error) { if (!signal.aborted) setMutationError(message(error) + ' Refresh saved settings before retrying if another administrator changed them.'); }
    finally { if (!signal.aborted) setBusy(false); }
  }
  return <Paper withBorder p="lg"><Stack>
    <Group justify="space-between" wrap="wrap"><Title order={3}>Saved desired settings</Title><Badge color={state?.draft ? 'yellow' : 'gray'}>{state ? state.draft ? 'Pending next start / restart' : 'No saved draft' : 'Unknown'}</Badge></Group>
    <Text size="sm">Save ordinary settings while the server runs. Saving only records a draft; it does not stop the server or change its active files. The next ordinary manager-controlled start or restart can apply it after confirmed stop, if its applied base and author authorization remain valid.</Text>
    <Text size="sm" c="dimmed">Use Server operations above for the existing restart and countdown controls. Version updates and rollbacks do not apply this draft. Direct OS autostart uses the last applied settings.</Text>
    {loading && <Loader size="sm" aria-label="Loading saved settings" />}
    {readError && <Alert color="red" role="alert">Saved settings could not be refreshed: {readError}. Last loaded data may be outdated.</Alert>}
    {mutationError && <Alert color="red" role="alert">{mutationError}</Alert>}
    {notice && <Alert color="green" role="status">{notice}</Alert>}
    {state && !state.draft && <Text>No saved desired settings are waiting. The applied settings above remain the manager's current record; this does not verify live game values.</Text>}
    {state?.draft && <>
      <Text size="sm">Current author authorization is checked when applying this draft. If the author was disabled, removed or demoted, applying it is blocked until an administrator explicitly re-saves or discards it. Author authorization status is not reported here.</Text>
      {state.stale && <Alert color="yellow" role="alert">This draft needs administrator review. Its applied base changed. Compare it with the applied settings above, then explicitly re-save against the current base or discard it. It cannot apply automatically.</Alert>}
      <SimpleGrid cols={{ base: 1, sm: 2 }}><Value label="Desired server name" value={state.draft.name} /><Value label="Desired memory" value={state.draft.memory_mib + ' MiB'} /><Value label="Desired server message" value={state.draft.config.motd} /><Value label="Desired difficulty / game mode" value={state.draft.config.difficulty + ' / ' + state.draft.config.game_mode} /><Value label="Desired maximum players" value={String(state.draft.config.max_players)} /><Value label="Desired PVP / online authentication" value={(state.draft.config.pvp ? 'On' : 'Off') + ' / ' + (state.draft.config.online_mode ? 'On' : 'Off')} /></SimpleGrid>
      <details><summary>All saved ordinary settings</summary><Text component="pre" size="sm" style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{JSON.stringify(state.draft.config, null, 2)}</Text></details>
      <Text size="xs" c="dimmed" style={{ overflowWrap: 'anywhere' }}>Revision {state.revision} · Saved {state.updated_at ? new Date(state.updated_at).toLocaleString() : 'time unavailable'} · Applied base {state.base_updated_at || 'unavailable'}</Text>
      {admin && <Text size="sm">Edit this draft or discard it before editing applied settings directly.</Text>}
    </>}
    {!admin && <Text size="sm" c="dimmed">Only administrators can save or discard desired settings.</Text>}
    <Group wrap="wrap"><Button variant="default" disabled={loading || busy} onClick={() => void reload(false)}>Refresh saved settings</Button>{admin && <Button disabled={!state || loading || !!readError || busy} onClick={edit}>{state?.draft ? 'Review / edit saved draft' : 'Prepare desired settings'}</Button>}{admin && state?.draft && <Button color="red" variant="light" disabled={loading || !!readError || busy} onClick={() => { if (!state.draft) return; setMutationError(''); setDiscarding({ revision: state.revision, name: state.draft.name }); }}>Discard saved draft</Button>}</Group>
    <Modal opened={!!editor && admin} onClose={() => setEditor(null)} title="Desired settings for next start / restart" size="lg">{editor && <Stack>
      <Alert color="blue">Saving this draft does not apply it to the running game. Port, world directory, software, version, Java and EULA are outside this draft. Changing the seed does not reset an existing world.</Alert>
      <details><summary>Compare current applied settings</summary><Value label="Applied name / memory" value={detail.instance.name + ' / ' + detail.instance.memory_mib + ' MiB'} /><Text component="pre" size="sm" style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{JSON.stringify(detail.config, null, 2)}</Text></details>
      {editor.stale && <Alert color="yellow">Compare with the applied values above before explicitly re-saving against the current applied base.</Alert>}
      {conflict && <Alert color="yellow" role="alert">The saved draft or applied settings changed. Your inputs are preserved. Reload the current revision and applied base, review the differences, then explicitly save again.<Button variant="light" disabled={loading} onClick={() => void reload(true)}>Reload current base, keep my edits</Button></Alert>}
      <InstanceForm detail={editor.detail} pending disabled={conflict || loading} submitLabel={editor.stale ? 'Re-save against current applied settings' : 'Save desired settings'} onCancel={() => setEditor(null)} onSubmit={async (draft, signal) => {
        const body: components['schemas']['SavePendingSettings'] = { name: draft.instance.name, memory_mib: draft.instance.memory_mib, config: draft.config, expected_revision: editor.revision, expected_instance_updated_at: editor.base };
        try {
          const result = await api<Pending>(path, { method: 'PUT', body: JSON.stringify(body), signal });
          if (!signal.aborted) { generation.current++; pendingRead.current?.abort(); setLoading(false); setState(result); setNotice('Desired settings saved as pending. They have not been applied to the game.'); }
        } catch (error) { if (!signal.aborted && error instanceof APIError && error.code === 'conflict') setConflict(true); throw error; }
      }} onSaved={async () => { setEditor(null); await refreshActive(lifetime.current.signal).catch(error => { if (!lifetime.current.signal.aborted) setReadError(message(error)); }); }} />
    </Stack>}</Modal>
    <Modal opened={discarding !== null && admin} onClose={() => !busy && setDiscarding(null)} title="Discard saved desired settings?" closeOnClickOutside={!busy} closeOnEscape={!busy}><Stack><Text style={{ overflowWrap: 'anywhere' }}>Discard “{discarding?.name}”, revision {discarding?.revision}?</Text><Text>This removes the saved draft only. It does not stop the server or change applied settings.</Text>{mutationError && <Alert color="red" role="alert">{mutationError}</Alert>}<Group wrap="wrap" justify="flex-end"><Button variant="default" disabled={busy} onClick={() => setDiscarding(null)}>Keep draft</Button><Button color="red" loading={busy} onClick={() => void discard()}>Confirm discard draft</Button></Group></Stack></Modal>
  </Stack></Paper>;
}
function Value({ label, value }: { label: string; value: string }) { return <div style={{ minWidth: 0 }}><Text size="sm" c="dimmed">{label}</Text><Text style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{value}</Text></div>; }

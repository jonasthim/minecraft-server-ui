import { createContext, useCallback, useContext, useEffect, useRef, useState } from 'react';
import { Alert, Anchor, Badge, Group, Loader, Paper, Stack, Text, Title } from '@mantine/core';
import { Link, NavLink, Outlet, useLocation, useParams } from 'react-router-dom';
import type { components } from '../../api/schema';
import { api } from '../../api/client';
import { useAuth } from '../auth/AuthProvider';
import { LifecyclePanel } from '../lifecycle/LifecyclePanel';
import { LiveFacts, useLiveInstances } from './live';

export type InstanceContext = { detail: components['schemas']['InstanceDetail']; refresh: (signal?: AbortSignal) => Promise<void> };
const Context = createContext<InstanceContext | null>(null);
export function useInstanceContext() { const value = useContext(Context); if (!value) throw new Error('InstanceLayout required'); return value; }
const sections = [['', 'Overview'], ['console', 'Console'], ['players', 'Players'], ['game-events', 'Deaths & advancements'], ['world', 'Worlds'], ['map', 'Saved map'], ['metrics', 'Metrics'], ['backups', 'Backups and restore'], ['content', 'Mods and plugins'], ['modpack', 'Modpacks'], ['updates', 'Updates and rollback'], ['schedules', 'Schedules']];
export function InstanceLayout() {
  const { instanceId = '' } = useParams(); const { user } = useAuth();
  return <InstanceScope key={instanceId + ':' + user?.id + ':' + user?.role} id={instanceId} />;
}
function InstanceScope({ id }: { id: string }) {
  const [detail, setDetail] = useState<InstanceContext['detail'] | null>(null);
  const [error, setError] = useState('');
  const lifetime = useRef(new AbortController());
  const { pathname } = useLocation();
  const base = '/instances/' + encodeURIComponent(id);
  const live = useLiveInstances([id])[id];
  const refresh = useCallback(async (signal?: AbortSignal) => {
    const own = lifetime.current.signal;
    const combined = AbortSignal.any([own, ...(signal ? [signal] : []), AbortSignal.timeout(10000)]);
    const result = await api<{ detail: InstanceContext['detail'] }>(base, { signal: combined });
    if (!combined.aborted) { setDetail(result.detail); setError(''); }
  }, [base]);
  useEffect(() => {
    const controller = new AbortController(); lifetime.current = controller;
    void refresh(controller.signal).catch(error => { if (!controller.signal.aborted) setError(error instanceof Error ? error.message : 'Server unavailable'); });
    return () => controller.abort();
  }, [refresh]);
  if (!detail) return error ? <Alert color="red" role="alert">{error}</Alert> : <Loader aria-label="Loading server" />;
  return <Context.Provider value={{ detail, refresh }}><Stack gap="lg">
    <Paper withBorder p="lg" className="app-instance-header"><Stack gap="sm">
      <Anchor component={Link} to="/" size="sm">Servers / {detail.instance.name}</Anchor>
      <Group justify="space-between" align="start"><div><Title order={2} style={{ overflowWrap: 'anywhere' }}>{detail.instance.name}</Title><Text size="sm" c="dimmed">{detail.instance.flavor} {detail.instance.minecraft_version} · Port {detail.instance.game_port}</Text></div><Badge color="gray">{detail.instance.id}</Badge></Group>
      <LiveFacts live={live} />
      <nav className="app-section-navigation" aria-label="Server features">{sections.map(([path, label]) => <NavLink key={path} to={base + (path ? '/' + path : '')} end={path === ''}>{label}</NavLink>)}</nav>
    </Stack></Paper>
    <details className="app-lifecycle-access" open={pathname === base || undefined}><summary>Server operations</summary><LifecyclePanel detail={detail} onChanged={refresh} /></details>
    <Outlet />
  </Stack></Context.Provider>;
}

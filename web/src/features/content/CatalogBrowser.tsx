import { useCallback, useEffect, useRef, useState } from 'react';
import { Button, Group, Stack, Text } from '@mantine/core';
import { api } from '../../api/client';
import type { components } from '../../api/schema';
type Project = components['schemas']['CatalogProjectMetadata'];
type Version = components['schemas']['CatalogVersionMetadata'];
type SearchPage = components['schemas']['CatalogSearchResult'];
type VersionPage = components['schemas']['CatalogVersionPage'];
type Page = Pick<SearchPage, 'offset' | 'limit' | 'total' | 'fetched_at'>;

export function CatalogPagination({ page, count, noun, busy, onPage }: { page: Page; count: number; noun: 'projects' | 'releases'; busy: boolean; onPage: (offset: number) => void }) {
  return <Stack gap="xs"><Text size="sm" c="dimmed" role="status">{count ? `${page.offset + 1}–${page.offset + count}` : '0'} of {page.total} publisher {noun} · page limit {page.limit} · fetched {new Date(page.fetched_at).toLocaleString()}</Text><Group wrap="wrap"><Button variant="default" disabled={busy || page.offset <= 0 || page.limit <= 0} onClick={() => onPage(0)}>First {noun}</Button><Button variant="default" disabled={busy || page.offset <= 0 || page.limit <= 0} onClick={() => onPage(Math.max(0, page.offset - page.limit))}>Previous {noun}</Button><Button variant="default" disabled={busy || count === 0 || page.limit <= 0 || page.offset + page.limit >= page.total} onClick={() => onPage(page.offset + page.limit)}>Next {noun}</Button></Group></Stack>;
}

// Discovery is cancellable independently of mutations. Every accepted page replaces
// the previous page; offsets and bounds come from publisher response metadata.
export function useCatalogBrowser(scope: string, filters: { kind: Project['kind']; minecraft?: string; loader?: string }) {
  const [result, setResult] = useState<SearchPage | null>(null);
  const [project, setProject] = useState<Project | null>(null);
  const [page, setPage] = useState<VersionPage | null>(null);
  const [exact, setExact] = useState<Version | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const request = useRef<AbortController | null>(null);
  const requestScope = useRef(scope);
  const submitted = useRef({ provider: 'modrinth', text: '' });
  const reset = useCallback(() => { request.current?.abort(); request.current = null; setBusy(false); setError(''); setResult(null); setProject(null); setPage(null); setExact(null); }, []);
  useEffect(() => { reset(); return () => { request.current?.abort(); }; }, [scope, reset]);
  function begin() { requestScope.current = scope; request.current?.abort(); const controller = new AbortController(); request.current = controller; setBusy(true); setError(''); return controller; }
  function query(offset: number) { const query = new URLSearchParams({ kind: filters.kind, offset: String(offset), limit: '20' }); if (filters.minecraft) query.set('minecraft', filters.minecraft); if (filters.loader) query.set('loader', filters.loader); return query; }
  async function run<T>(controller: AbortController, path: string, accept: (value: T) => void) {
    try { const value = await api<T>(path, { signal: controller.signal }); if (!controller.signal.aborted) accept(value); }
    catch (error) { if (!controller.signal.aborted) setError(error instanceof Error ? error.message : 'Catalog request failed.'); }
    finally { if (!controller.signal.aborted) setBusy(false); }
  }
  async function search(provider: string, text: string, offset = 0) {
    const controller = begin(); submitted.current = { provider, text }; setProject(null); setPage(null); setExact(null); setResult(null);
    const parameters = query(offset); parameters.set('q', text);
    await run<SearchPage>(controller, '/catalog/' + provider + '/search?' + parameters, value => {
      if (value.source !== provider || value.projects.some(item => item.source !== provider || item.kind !== filters.kind)) { setError('Publisher returned search results for another provider or content kind.'); return; }
      setResult(value);
    });
  }
  async function releases(selected: Project, offset = 0) {
    const controller = begin(); setProject(selected); setPage(null); setExact(null);
    await run<VersionPage>(controller, '/catalog/' + selected.source + '/projects/' + encodeURIComponent(selected.id) + '/versions?' + query(offset), value => {
      if (value.source !== selected.source || value.versions.some(version => version.source !== selected.source || version.project_id !== selected.id)) { setError('Publisher returned releases for another project.'); return; }
      setPage(value);
    });
  }
  async function identity(provider: Project['source'], projectId: string, versionId?: string) {
    const controller = begin(); setProject(null); setPage(null); setExact(null);
    const base = '/catalog/' + provider + '/projects/' + encodeURIComponent(projectId);
    try {
      const selected = await api<Project>(base, { signal: controller.signal });
      if (controller.signal.aborted) return;
      if (selected.source !== provider || selected.id !== projectId || selected.kind !== filters.kind) throw new Error('Publisher project identity or content kind does not match the dependency.');
      setProject(selected);
      if (versionId) {
        const version = await api<Version>(base + '/versions/' + encodeURIComponent(versionId), { signal: controller.signal });
        if (controller.signal.aborted) return;
        if (version.source !== provider || version.project_id !== projectId || version.id !== versionId) throw new Error('Publisher returned a different dependency release.');
        setExact(version);
      } else {
        const value = await api<VersionPage>(base + '/versions?' + query(0), { signal: controller.signal });
        if (!controller.signal.aborted) {
          if (value.source !== provider || value.versions.some(version => version.source !== provider || version.project_id !== projectId)) throw new Error('Publisher returned releases for another project.');
          setPage(value);
        }
      }
    } catch (error) { if (!controller.signal.aborted) setError(error instanceof Error ? error.message : 'Dependency lookup failed.'); }
    finally { if (!controller.signal.aborted) setBusy(false); }
  }
  function back() { request.current?.abort(); setBusy(false); setError(''); setProject(null); setPage(null); setExact(null); }
  const visible = requestScope.current === scope;
  return { result: visible ? result : null, project: visible ? project : null, page: visible ? page : null, exact: visible ? exact : null, versions: visible ? (exact ? [exact] : page?.versions ?? null) : null, busy, error, reset, back, search, releases, identity, searchPage: (offset: number) => search(submitted.current.provider, submitted.current.text, offset) };
}

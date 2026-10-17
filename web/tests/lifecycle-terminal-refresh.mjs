// Focused intercepted-API browser regression. Run with a Vite server on :15173:
// node web/tests/lifecycle-terminal-refresh.mjs
import assert from 'node:assert/strict';
import { chromium } from 'playwright-core';

const base = process.env.LIFECYCLE_CHECK_URL ?? 'http://127.0.0.1:15173';
const browser = await chromium.launch({ headless: true });
const results = [];
try {
  for (const scenario of ['success', 'lifecycle failure', 'detail failure']) {
    const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    let terminal = false;
    let detailReads = 0;
    let heldRefresh;
    let release;
    const deferred = new Promise(resolve => { release = resolve; });
    const capability = { available: true, method: 'server_property', reason: 'Browser fixture' };
    const detail = {
      instance: { id: 'alpha', name: 'Alpha', flavor: 'fabric', minecraft_version: '1.20.1', loader_version: '', java_major: 17, java_runtime_id: '', memory_mib: 1024, game_port: 25565, created_at: '2026-10-17T00:00:00Z', updated_at: '2026-10-17T00:00:00Z' },
      config: { motd: 'Fixture', difficulty: 'normal', game_mode: 'survival', max_players: 20, online_mode: true, pvp: true, view_distance: 10, simulation_distance: 10, level_name: 'world', level_seed: '', extra: {} },
      eula: { accepted: false }, provisioning: 'not_installed',
      settings: { legacy_numeric_enums: false, simulation_distance: capability, pvp: capability, desired_runtime: [], seed_reason: 'Fixture' },
    };
    const state = { installed: false, process: 'stopped', process_running: false, readiness: 'stopped', autostart_desired: false, autostart_applied: false };
    const job = { id: 'install-fixture', instance_id: 'alpha', kind: 'lifecycle', state: 'queued', phase: 'Waiting', progress: 0, error: '', cancellable: false, cancel_requested: false, requested_by: 1, created_at: '2026-10-17T00:00:00Z' };
    await page.route('**/api/v1/**', async route => {
      const url = new URL(route.request().url());
      const path = url.pathname.replace('/api/v1', '');
      const json = body => route.fulfill({ json: body });
      const fail = message => route.fulfill({ status: 503, json: { error: { code: 'fixture_failure', message } } });
      if (path === '/auth/status') return json({ needs_setup: false, local_login_enabled: true, oidc_enabled: false });
      if (path === '/auth/me') return json({ user: { id: 1, username: 'admin', display_name: 'Admin', email: '', role: 'admin', disabled: false, local: true }, csrf_token: 'fixture' });
      if (path === '/instances/alpha') {
        detailReads++;
        if (terminal && scenario === 'detail failure') return fail('Final detail refresh unavailable');
        return json({ detail: terminal ? { ...detail, instance: { ...detail.instance, loader_version: '0.16.0' }, provisioning: 'installed' } : detail });
      }
      if (path === '/lifecycle/catalog') return json({ versions: ['1.20.1'], loaders: ['0.16.0'], qualification: 'Fixture only' });
      if (path === '/instances/alpha/lifecycle/preview') return json({ id: 'preview', selection: { flavor: 'fabric', minecraft_version: '1.20.1', loader_version: '0.16.0' }, requirements: { java_major: 17, min_java_major: 17, requires_installer: false }, artifacts: [], warnings: [], expires_at: '2100-01-01T00:00:00Z' });
      if (path === '/instances/alpha/lifecycle' && route.request().method() === 'POST') return json({ job });
      if (path === '/jobs/install-fixture') {
        terminal = true;
        return json({ job: { ...job, state: scenario === 'success' ? 'succeeded' : 'failed', phase: 'Finished', progress: 100, error: scenario === 'success' ? '' : 'Original operation failure remains visible' } });
      }
      if (path === '/instances/alpha/lifecycle') {
        if (terminal) {
          heldRefresh = true;
          await deferred;
          if (scenario === 'lifecycle failure') return fail('Final lifecycle refresh unavailable');
          return json({ ...state, installed: true });
        }
        return json(state);
      }
      throw new Error('Unexpected API request: ' + path);
    });
    // Keep the independent 5-second status poll outside this test's evidence.
    await page.clock.install();
    await page.goto(base + '/instances/alpha');
    await page.getByRole('button', { name: 'Install server files', exact: true }).click();
    await page.getByRole('button', { name: 'Load official builds and loaders' }).click();
    await page.getByRole('combobox', { name: 'Loader version', exact: true }).click();
    await page.getByRole('option', { name: '0.16.0', exact: true }).click();
    await page.getByRole('button', { name: 'Resolve verified install preview' }).click();
    await page.getByRole('button', { name: 'Confirm installation' }).click();
    await page.getByText('queued', { exact: true }).waitFor();
    const initialDetailReads = detailReads;
    await page.clock.runFor(2000);
    await page.getByText(scenario === 'success' ? 'succeeded' : 'failed', { exact: true }).waitFor();
    assert.equal(heldRefresh, true, 'terminal lifecycle response was not deferred');
    if (scenario !== 'success') await page.getByRole('alert').filter({ hasText: 'Original operation failure remains visible' }).waitFor();
    release();
    if (scenario === 'lifecycle failure') await page.getByRole('alert').filter({ hasText: 'Final lifecycle refresh unavailable' }).waitFor({ timeout: 3000 });
    if (scenario === 'detail failure') await page.getByRole('alert').filter({ hasText: 'Final detail refresh unavailable' }).first().waitFor({ timeout: 3000 });
    if (scenario !== 'lifecycle failure') await page.getByText('Installed', { exact: true }).waitFor({ timeout: 3000 });
    // A successful detail refresh changes the persisted pinned-loader display.
    if (scenario !== 'detail failure') {
      await page.getByRole('button', { name: /^(Install server files|Reinstall pinned version)$/ }).click({ timeout: 3000 });
      await page.getByText('Pinned build/loader: 0.16.0', { exact: true }).waitFor({ timeout: 3000 });
    }
    assert.equal(detailReads, initialDetailReads + 1, 'terminal completion must refresh the parent detail once');
    assert.deepEqual(errors, []);
    results.push({ scenario, terminalVisibleBeforeRelease: true, detailReads, independentPollRan: false });
    await context.close();
  }
  console.log(JSON.stringify(results, null, 2));
} finally { await browser.close(); }

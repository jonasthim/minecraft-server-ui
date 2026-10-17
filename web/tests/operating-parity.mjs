// Isolated browser evidence. Actual setup/create uses a disposable empty manager,
// and intercepted telemetry is explicitly separate from real game qualification.
// TASK26_URL=http://127.0.0.1:15226 node web/tests/operating-parity.mjs
import assert from 'node:assert/strict';
import { mkdir } from 'node:fs/promises';
import { chromium } from 'playwright-core';
const base = process.env.TASK26_URL ?? 'http://127.0.0.1:15226';
const shots = process.env.TASK26_SCREENSHOTS ?? '/tmp/task26-screenshots';
await mkdir(shots, { recursive: true });
const browser = await chromium.launch({ headless: true });
const results = [];
try {
  if (process.env.TASK26_ACTUAL === '1') {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
    const page = await context.newPage();
    const errors = []; page.on('pageerror', error => errors.push(error.message));
    await page.goto(base);
    await page.getByRole('heading', { name: 'Create your administrator' }).waitFor();
    await page.getByRole('textbox', { name: /^Username/ }).fill('task26-admin');
    await page.getByLabel('Display name', { exact: true }).fill('Operating review');
    await page.getByLabel(/^Password/).fill('Disposable-Task26-Password');
    await page.getByLabel(/^Confirm password/).fill('Disposable-Task26-Password');
    await page.getByRole('button', { name: 'Create administrator', exact: true }).click();
    await page.getByRole('heading', { name: 'No servers configured' }).waitFor();
    await page.evaluate(() => scrollTo(0, 0));
    await page.screenshot({ path: shots + '/actual-empty-desktop.png', fullPage: true });
    await page.getByRole('button', { name: 'Create server', exact: true }).click();
    await page.getByRole('textbox', { name: /^Server name/ }).fill('Operating review');
    await page.getByRole('textbox', { name: /^Server id/ }).fill('operating-review');
    await page.getByRole('combobox', { name: /^Minecraft version/ }).click();
    const version = page.getByRole('option', { name: '1.21.5', exact: true });
    await version.waitFor({ timeout: 30000 }); await version.click();
    // EULA remains unchecked. Creation does not install or start Minecraft.
    assert.equal(await page.getByRole('checkbox', { name: /EULA/ }).isChecked(), false);
    await page.getByRole('button', { name: 'Create server', exact: true }).last().click();
    await page.getByRole('link', { name: 'Open server', exact: true }).click();
    await page.getByRole('navigation', { name: 'Server features' }).waitFor();
    await page.getByText('Not accepted', { exact: true }).waitFor();
    await page.evaluate(() => scrollTo(0, 0));
    await page.screenshot({ path: shots + '/actual-overview-desktop.png', fullPage: true });
    await page.getByRole('navigation', { name: 'Server features' }).getByRole('link', { name: 'Metrics', exact: true }).click();
    await page.getByRole('heading', { name: 'JVM metrics', exact: true }).waitFor();
    await page.getByRole('combobox', { name: 'Appearance', exact: true }).click();
    await page.getByRole('option', { name: 'Dark', exact: true }).click();
    await page.reload();
    await page.getByRole('navigation', { name: 'Server features' }).waitFor();
    assert.equal(await page.locator('html').getAttribute('data-mantine-color-scheme'), 'dark');
    await page.setViewportSize({ width: 390, height: 844 });
    await page.getByRole('navigation', { name: 'Server features' }).getByRole('link', { name: 'Overview', exact: true }).click();
    await page.getByRole('heading', { name: 'Resource trends', exact: true }).waitFor();
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    await page.evaluate(() => scrollTo(0, 0));
    await page.screenshot({ path: shots + '/actual-overview-mobile-dark.png', fullPage: true });
    await page.getByRole('navigation', { name: 'Server features' }).getByRole('link', { name: 'Console', exact: true }).click();
    await page.getByText('No captured output', { exact: false }).waitFor();
    await page.evaluate(() => scrollTo(0, 0));
    await page.screenshot({ path: '/home/thim/.cache/dilla-tmp/task27-console-actual-empty.png', fullPage: true });
    console.log('ACTUAL_CONSOLE_OBSERVATIONS:', await page.locator('main').innerText());
    await page.getByRole('button', { name: 'Open navigation' }).click();
    await page.getByRole('combobox', { name: 'Selected server', exact: true }).click();
    await page.getByRole('option', { name: 'Operating review', exact: true }).click();
    assert.deepEqual(errors, []); results.push({ actual: 'fresh DB admin/create, EULA unchecked, persistent section navigation, theme reload, 390px sidebar', errors });
    await context.close();
  }
  const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const page = await context.newPage(); const errors = []; page.on('pageerror', error => errors.push(error.message));
  const now = Date.now(); let oldRelease; let holdAlpha = false; let held = false; let active = 0; let maximum = 0; let holdLifecycle = false; let lifecycleReadsHeld = 0; const lifecycleReleases = []; let role = 'admin';
  const capability = { available: true, method: 'server_property', reason: 'Intercepted telemetry fixture' };
  const detail = id => ({ instance: { id, name: id === 'alpha' ? 'Alpha' : 'Beta', flavor: 'vanilla', minecraft_version: '1.21.5', java_major: 21, java_runtime_id: '', memory_mib: 2048, game_port: id === 'alpha' ? 25565 : 25566, created_at: new Date(now).toISOString(), updated_at: new Date(now).toISOString() }, config: { motd: 'Intercepted fixture', difficulty: 'normal', game_mode: 'survival', max_players: 20, online_mode: true, pvp: true, view_distance: 10, simulation_distance: 10, level_name: 'world', level_seed: '', extra: {} }, eula: { accepted: false }, provisioning: 'not_installed', settings: { legacy_numeric_enums: false, simulation_distance: capability, pvp: capability, desired_runtime: [], seed_reason: 'Fixture' } });
  const point = (index, gap = false) => ({ collected_at: new Date(now - (5 - index) * 30000).toISOString(), cpu_percent: gap ? null : 10 + index, resident_bytes: gap ? null : 1000000, private_bytes: null, disk_bytes: gap ? null : 2000000, players: gap ? null : 2, counts: { samples: gap ? 0 : 1, cpu: gap ? 0 : 1, resident: gap ? 0 : 1, private: 0, disk: gap ? 0 : 1, players: gap ? 0 : 1 }, expected: 1, gap, sources: [] });
  await page.route('**/api/v1/**', async route => {
    const url = new URL(route.request().url()); const path = url.pathname.replace('/api/v1', '');
    const json = body => route.fulfill({ json: body });
    if (path === '/auth/status') return json({ needs_setup: false, local_login_enabled: true, oidc_enabled: false });
    if (path === '/auth/me') return json({ user: { id: 1, username: 'admin', display_name: 'Fixture admin', email: '', role, disabled: false, local: true }, csrf_token: 'fixture' });
    if (path === '/auth/logout') { role = 'viewer'; return route.fulfill({ status: 204 }); }
    if (path === '/instances') return json({ instances: [detail('alpha'), detail('beta')] });
    if (/^\/instances\/(alpha|beta)$/.test(path)) return json({ detail: detail(path.split('/')[2]) });
    if (path.endsWith('/lifecycle')) { if (holdLifecycle) { lifecycleReadsHeld++; await new Promise(resolve => lifecycleReleases.push(resolve)); } return json({ installed: false, process: 'running', process_running: true, readiness: 'starting', autostart_desired: false, autostart_applied: null }); }
    if (path.endsWith('/players')) return json({ snapshot: { instance_id: path.split('/')[2], generation: 'fixture', observed_at: new Date(now - (path.includes('beta') ? 600000 : 0)).toISOString(), known: true, complete: true, count: path.includes('beta') ? 99 : 2, players: [], gap: false, reason: '' } });
    if (path.endsWith('/metrics')) {
      active++; maximum = Math.max(maximum, active);
      if (holdAlpha && path.includes('alpha')) { held = true; await new Promise(resolve => { oldRelease = resolve; }); }
      active--;
      return json({ instance_id: path.split('/')[2], resolution: 'raw', interval_seconds: 30, cpu_convention: 'Intercepted fixture, not game evidence', points: [point(0), point(1), point(2, true), point(3), point(4), point(5)] });
    }
    if (path.includes('/players/history')) return json({ sessions: [], gaps: [] });
    if (path.startsWith('/instances/%')) return route.fulfill({ status: 400, json: { error: { code: 'invalid_id', message: 'Invalid server identifier' } } });
    throw new Error('Unexpected intercepted request: ' + path);
  });
  await page.clock.install();
  await page.goto(base);
  await page.getByText('Unknown fleet total · partial or missing observations', { exact: false }).waitFor();
  assert.equal(await page.getByText('Online players: 101', { exact: true }).count(), 0);
  await page.getByRole('link', { name: 'Open server', exact: true }).first().click();
  const nav = page.getByRole('navigation', { name: 'Server features' }); await nav.waitFor();
  await page.locator('svg[data-field="cpu_percent"]').waitFor();
  assert.equal(await page.locator('svg[data-field="cpu_percent"] polyline').count(), 2, 'gap must split CPU line');
  await page.getByText('Readiness: starting', { exact: true }).waitFor();
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
  await page.evaluate(() => scrollTo(0, 0));
  await page.screenshot({ path: shots + '/intercepted-gap-mobile.png', fullPage: true });
  // With the next lifecycle response held, the five-second status poll must not overlap.
  holdLifecycle = true;
  await page.clock.runFor(11000);
  assert.equal(lifecycleReadsHeld, 1, 'slow lifecycle poll must remain serialized before header refresh at15s');
  holdLifecycle = false; lifecycleReleases.forEach(resolve => resolve());
  await page.clock.resume();
  // Hold Alpha's next metrics request while switching via the actual sidebar.
  holdAlpha = true;
  await nav.getByRole('link', { name: 'Metrics', exact: true }).click();
  await page.waitForFunction(() => document.body.innerText.includes('Loading metrics'));
  await page.getByRole('button', { name: 'Open navigation' }).click();
  await page.getByRole('combobox', { name: 'Selected server', exact: true }).click();
  await page.getByRole('option', { name: 'Beta', exact: true }).click();
  await page.getByRole('heading', { name: 'Beta', exact: true }).waitFor();
  assert.equal(held, true); oldRelease?.();
  await page.getByText('Player coverage incomplete or stale.', { exact: true }).waitFor();
  assert.equal(await page.getByRole('heading', { name: 'Alpha', exact: true }).count(), 0);
  assert.equal(await page.locator('main').getByText('99', { exact: true }).count(), 0, 'stale roster must not be shown as current count');
  await page.evaluate(() => scrollTo(0, 0));
  await page.screenshot({ path: shots + '/intercepted-beta-stale-mobile.png', fullPage: true });
  await page.getByRole('button', { name: 'Stop server', exact: true }).click();
  await page.getByRole('dialog').waitFor();
  // Simulate account refresh while a modal is open; the global auth event must unmount it.
  await page.getByRole('button', { name: 'Sign out', exact: true, includeHidden: true }).evaluate(button => button.click());
  await page.getByRole('heading', { name: 'Beta', exact: true }).waitFor();
  assert.equal(await page.getByRole('dialog').count(), 0, 'old administrator dialog must clear on role switch');
  assert.equal(await page.getByRole('button', { name: 'Stop server', exact: true }).count(), 0, 'viewer must have no lifecycle mutation');
  assert(maximum <= 6, 'fleet telemetry concurrency cap');
  // Vite rejects malformed URL escapes before SPA delivery; use client navigation
  // to exercise the application's route parsing itself.
  await page.evaluate(() => { history.pushState(null, '', '/instances/%'); dispatchEvent(new PopStateEvent('popstate')); });
  await page.getByRole('alert').filter({ hasText: 'Invalid server identifier' }).waitFor();
  await page.getByRole('combobox', { name: 'Appearance', exact: true }).waitFor();
  assert.deepEqual(errors, []);
  results.push({ serializedSlowLifecycleReads: lifecycleReadsHeld, roleSwitchClearedAdminDialog: true, intercepted: 'partial fleet, process/readiness distinction, gap chart, delayed Alpha response after sidebar switch, stale Beta roster, 390px', maxConcurrentMetricRequests: maximum, errors });
  await context.close();
  console.log(JSON.stringify({ results, screenshots: shots }, null, 2));
} finally { await browser.close(); }

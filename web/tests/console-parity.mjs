// Intercepted API/stream browser evidence, not a real game or native-service qualification.
import assert from 'node:assert/strict';
import { chromium } from 'playwright-core';
const base = process.env.CONSOLE_CHECK_URL ?? 'http://127.0.0.1:15175';
const browser = await chromium.launch({ headless: true });
const state = { logs_available: true, process_running: false, ready_for_use: false, reason: 'Intercepted fixture: stopped server', logs_reason: '' };
const line = (id, message = `line ${id}`) => ({ id, message, created_at: '2026-10-17T00:00:00Z', gap: false });
try {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 }, acceptDownloads: true });
  const page = await context.newPage();
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.addInitScript(() => {
    window.streams = [];
    window.EventSource = class extends EventTarget {
      constructor(url) { super(); this.url = url; window.streams.push(this); queueMicrotask(() => this.onopen?.()); }
      close() { this.closed = true; }
    };
    window.emitConsole = value => {
      const stream = window.streams.at(-1);
      if (!stream.closed) stream.dispatchEvent(new MessageEvent('console', { data: JSON.stringify(value) }));
    };
  });
  let role = 'viewer';
  let mode = 'normal';
  let releaseHeld;
  let held;
  async function waitForHeldRequest() {
    for (let i = 0; i < 200; i++) { if (held) return; await new Promise(resolve => setTimeout(resolve, 10)); }
    throw Error('Expected held console request was not made.');
  }
  const requests = [];
  await page.route('**/api/v1/**', async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace('/api/v1', '');
    const json = body => route.fulfill({ json: body });
    if (path === '/auth/status') return json({ needs_setup: false, local_login_enabled: true, oidc_enabled: false });
    if (path === '/auth/me') return json({ user: { id: 1, username: 'fixture', role, disabled: false }, csrf_token: 'fixture' });
    if (path.endsWith('/console/commands')) return json({ job: { id: 'console-fixture', state: 'queued', kind: 'console_command' } });
    if (path === '/jobs/console-fixture') return json({ job: { id: 'console-fixture', state: 'succeeded', kind: 'console_command' } });
    if (path === '/jobs/console-fixture/logs') return json({ logs: [{ message: 'Intercepted server acknowledgement' }] });
    if (path.endsWith('/console/status')) return json({ state });
    if (path.endsWith('/console/logs')) {
      requests.push(path + url.search);
      if (mode === 'failure') return route.fulfill({ status: 503, json: { error: { message: 'Fixture history unavailable' } } });
      if (mode === 'hold') { held = true; await new Promise(resolve => { releaseHeld = resolve; }); }
      const after = Number(url.searchParams.get('after'));
      const beta = path.includes('/beta/');
      const lines = mode === 'empty' ? [] : Array.from({ length: mode === 'limit' ? 2000 : 1100 }, (_, i) => line(i + 1, `${mode === 'limit' ? 'x'.repeat(4096) : beta ? 'Beta' : 'Alpha'} ${i % 2 ? '[WARN] token=[REDACTED]' : '[INFO] ready'} ${i + 1}`)).filter(item => item.id > after).slice(0, 500);
      if (mode === 'malformed') lines.reverse();
      return json({ lines, next_cursor: lines.at(-1)?.id ?? after, gap: mode === 'gap' });
    }
    // Allow integration's instance shell to render without inventing game evidence.
    if (path === '/instances') return json({ instances: [] });
    if (/\/instances\/(alpha|beta)$/.test(path)) return json({ detail: {
      instance: { id: path.split('/').at(-1), name: path.endsWith('beta') ? 'Beta' : 'Alpha', flavor: 'vanilla', minecraft_version: '1.20.1', java_major: 17, memory_mib: 1024, game_port: 25565 },
      config: { motd: 'Fixture', difficulty: 'normal', game_mode: 'survival', max_players: 20, online_mode: true, pvp: true, view_distance: 10, simulation_distance: 10, level_name: 'world', level_seed: '', extra: {} },
      eula: { accepted: false }, provisioning: 'not_installed',
      settings: { legacy_numeric_enums: false, desired_runtime: [], seed_reason: 'Intercepted fixture' },
    } });
    if (path.endsWith('/lifecycle')) return json({ installed: false, process: 'stopped', readiness: 'stopped', process_running: false });
    return route.fulfill({ status: 404, json: { error: { message: 'Outside console fixture: ' + path } } });
  });
  await page.goto(base + '/instances/alpha/console');
  await page.getByLabel('Filter console text').waitFor({ timeout: 5000 });
  const output = page.getByRole('region', { name: 'Server console output' });
  await page.getByText(/^500 buffered lines · cursor/).waitFor();
  assert.equal(await page.getByRole('button', { name: 'Run command', exact: true }).count(), 0);
  // Simulate a stream disconnect and resumed duplicate snapshot; this labels client behavior only.
  await page.evaluate(state => window.emitConsole({ page: { lines: [], next_cursor: 1100, gap: false }, state: { ...state, ready_for_use: true } }), state);
  await page.getByText('Ready for use', { exact: true }).waitFor();
  await page.evaluate(() => window.streams.at(-1).onerror());
  await page.getByText('Disconnected; reconnecting and checking access', { exact: true }).waitFor();
  await page.getByText('Previous readiness observation is stale.', { exact: false }).waitFor();
  assert.equal(await page.getByText('Ready for use', { exact: true }).count(), 0);
  await page.evaluate(state => window.emitConsole({ page: { lines: [{ id: 1100, message: 'Alpha [WARN] token=[REDACTED] 1100', created_at: '2026-10-17T00:00:00Z', gap: false }], next_cursor: 1100, gap: false }, state }), state);
  await page.getByText('Console stream connected', { exact: true }).waitFor();
  assert.equal(await output.locator('[data-console-line]').count(), 500);
  await page.getByLabel('Filter console text').fill('warn');
  assert.equal(await output.locator('[data-console-line]').count(), 250);
  const ids = await output.locator('[data-console-line]').evaluateAll(nodes => nodes.map(node => Number(node.dataset.consoleLine)));
  assert.deepEqual(ids, [...ids].sort((a, b) => a - b));
  await page.getByLabel('Filter console text').fill('');
  await output.evaluate(node => { node.scrollTop = 0; node.dispatchEvent(new Event('scroll')); });
  await page.waitForFunction(() => !document.querySelector('input[role=\"switch\"]').checked);
  assert.equal(await page.getByLabel('Follow live output').isChecked(), false);
  await page.evaluate(state => window.emitConsole({ page: { lines: [{ id: 1101, message: '[ERROR] <script>unsafe()</script>', created_at: '2026-10-17T00:00:00Z', gap: true }], next_cursor: 1101, gap: true }, state }), state);
  assert.equal(await output.evaluate(node => node.scrollTop), 0);
  assert.equal(await output.locator('script').count(), 0);
  await page.getByText('Console history has gaps', { exact: true }).waitFor();
  await page.getByLabel('Follow live output').check();
  await page.waitForFunction(() => { const node = document.querySelector('[aria-label="Server console output"]'); return node.scrollTop > 0; });
  await page.getByRole('button', { name: 'Pause updates', exact: true }).click();
  assert.equal(await page.evaluate(() => window.streams.at(-1).closed), true);
  await page.getByRole('button', { name: 'Browse retained history', exact: true }).click();
  await page.getByText('Retained page 1', { exact: false }).waitFor();
  await page.getByRole('button', { name: 'Next page', exact: true }).click();
  await page.getByText('Retained page 2', { exact: false }).waitFor();
  assert.equal(await output.locator('[data-console-line]').first().getAttribute('data-console-line'), '501');
  await page.getByRole('button', { name: 'Previous page', exact: true }).click();
  await page.getByText('Retained page 1', { exact: false }).waitFor();
  await page.getByLabel('Filter console text').fill('warn');
  const downloadEvent = page.waitForEvent('download');
  await page.getByRole('button', { name: 'Download retained output', exact: true }).click();
  const download = await downloadEvent;
  assert.match(download.suggestedFilename(), /^alpha-retained-console/);
  const stream = await download.createReadStream();
  let downloadText = '';
  for await (const chunk of stream) downloadText += chunk;
  assert.match(downloadText, /Not a complete raw log/);
  assert.match(downloadText, /Alpha/);
  assert.doesNotMatch(downloadText, /Beta/);
  assert.match(downloadText, /REDACTED/);
  assert.doesNotMatch(downloadText, /<script>/);
  assert.ok(downloadText.indexOf('Alpha [INFO] ready 1\n') < downloadText.indexOf('Alpha [WARN] token=[REDACTED] 2\n'));
  // In-flight export is cancelled by a client-side server change, never delivered under Beta.
  mode = 'hold'; held = false;
  await page.getByRole('button', { name: 'Download retained output', exact: true }).click();
  await waitForHeldRequest();
  let lateDownloads = 0;
  page.on('download', () => lateDownloads++);
  await page.evaluate(() => { history.pushState(null, '', '/instances/beta/console'); dispatchEvent(new PopStateEvent('popstate')); });
  mode = 'normal'; releaseHeld();
  await page.getByText(/^500 buffered lines · cursor/).waitFor();
  assert.doesNotMatch(await output.innerText(), /Alpha/);
  await page.waitForTimeout(100);
  assert.equal(lateDownloads, 0);
  assert.equal(await page.getByLabel('Follow live output').isChecked(), true);
  await page.getByLabel('Filter console text').focus();
  await page.keyboard.type('no-match');
  await page.getByText('No lines match this filter.').waitFor();
  await page.keyboard.press('Tab');
  await page.keyboard.press('Space');
  assert.equal(await page.getByLabel('Follow live output').isChecked(), false);
  await page.keyboard.press('Space');
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
  await page.screenshot({ path: '/home/thim/.cache/dilla-tmp/task27-console-mobile.png', fullPage: true });
  // Accelerate the browser's timeout signal for a deterministic held HTTP request.
  // This exercises production timeout handling without waiting fifteen real seconds.
  await page.evaluate(() => { const nativeTimeout = AbortSignal.timeout.bind(AbortSignal); window.restoreTimeout = () => { AbortSignal.timeout = nativeTimeout; }; AbortSignal.timeout = () => nativeTimeout(50); });
  mode = 'hold'; held = false;
  await page.getByRole('button', { name: 'Browse retained history', exact: true }).click();
  await waitForHeldRequest();
  await page.getByText(/timed out|timeout/i).waitFor();
  assert.equal(await page.getByRole('button', { name: 'Browse retained history', exact: true }).isEnabled(), true);
  mode = 'normal'; releaseHeld();
  mode = 'hold'; held = false;
  await page.getByRole('button', { name: 'Download retained output', exact: true }).click();
  await waitForHeldRequest();
  await page.getByText(/Download failed.*(?:timed out|timeout)/i).waitFor();
  assert.equal(await page.getByRole('button', { name: 'Download retained output', exact: true }).isEnabled(), true);
  mode = 'normal'; releaseHeld();
  await page.evaluate(() => window.restoreTimeout());
  mode = 'limit';
  await page.reload();
  await page.getByText(/^500 buffered lines · cursor 2000/).waitFor();
  const boundedEvent = page.waitForEvent('download');
  await page.getByRole('button', { name: 'Download retained output', exact: true }).click();
  const boundedDownload = await boundedEvent;
  const boundedStream = await boundedDownload.createReadStream();
  let boundedText = '';
  for await (const chunk of boundedStream) boundedText += chunk;
  assert.match(boundedText, /LIMIT REACHED: excerpt truncated/);
  assert.ok(Buffer.byteLength(boundedText) < 4 * 1024 * 1024 + 1024);
  mode = 'malformed';
  await page.reload();
  await page.getByText('Invalid console history line.', { exact: true }).waitFor();
  mode = 'empty';
  await page.reload();
  await page.getByText('No captured output yet.', { exact: false }).waitFor();
  role = 'operator';
  await page.reload();
  await page.getByRole('button', { name: 'Run command', exact: true }).click();
  await page.getByText('Server acknowledged command.', { exact: true }).waitFor();
  await page.getByText('Intercepted server acknowledgement', { exact: true }).waitFor();
  await page.getByRole('combobox', { name: 'Command', exact: true }).click();
  assert.equal(await page.getByRole('option', { name: 'Read world seed' }).count(), 0);
  await page.keyboard.press('Escape');
  mode = 'failure';
  await page.reload();
  await page.getByText('Fixture history unavailable', { exact: true }).waitFor();
  assert.equal(errors.length, 0, errors.join('\n'));
  assert.ok(requests.some(path => path.includes('after=500')));
  console.log('PASS intercepted console: filter/order, safe semantic lines, scroll/follow/pause, retained paging/export scope, cancelled wrong-instance export, duplicate reconnect snapshot, 4MiB limit, malformed rejection, 390px keyboard, operator command acknowledgement, stale readiness, finite history/export timeouts, empty/failure');
  await context.close();
} finally { await browser.close(); }

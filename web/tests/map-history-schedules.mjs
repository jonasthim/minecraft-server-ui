// Intercepted client behavior only. No native server, game, world or release qualification.
// npm --prefix web run dev -- --host 127.0.0.1 --port 15531 --strictPort
import assert from 'node:assert/strict';
import { chromium } from 'playwright-core';
const base = process.env.TASK31C_URL || 'http://127.0.0.1:15531';
const browser = await chromium.launch({ headless: true });
const results = [];
try {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const page = await context.newPage(); page.setDefaultTimeout(6000);
  const errors = []; page.on('pageerror', error => errors.push(error.message));
  let role = 'admin'; let scheduleRecords = []; const requests = []; const mutations = [];
  const now = '2026-10-17T12:00:00Z';
  const capability = { available: true, method: 'server_property', reason: 'Intercepted client fixture' };
  const detail = id => ({ instance: { id, name: id, flavor: 'vanilla', minecraft_version: '1.21.5', java_major: 21, memory_mib: 2048, game_port: id === 'alpha' ? 25565 : 25566, created_at: now, updated_at: now }, config: { motd: 'fixture', difficulty: 'normal', game_mode: 'survival', max_players: 20, online_mode: true, pvp: true, view_distance: 10, simulation_distance: 10, level_name: 'world', extra: {} }, eula: { accepted: false }, provisioning: 'not_installed', settings: { legacy_numeric_enums: false, simulation_distance: capability, pvp: capability, desired_runtime: [] } });
  const event = (id, kind, name, observed_at, message) => ({ id, kind, player: { name, authority: 'name_only' }, observed_at, message, source: 'redacted_native_console', source_cursor: id, source_version: 'unknown', confidence: kind === 'source_gap' ? 'gap' : 'native_log_pattern', parser_version: 'fixture', gap: kind === 'source_gap' });
  let holdHistory = false; let heldHistory = false; let releaseHistory; let bulkEvents = false; let failHistory = false; let releaseView; let heldView = false; let holdView = false;
  await page.route('**/api/v1/**', async route => {
    const url = new URL(route.request().url()); const path = url.pathname.replace('/api/v1', ''); requests.push(path + url.search);
    const json = body => route.fulfill({ json: body });
    if (path === '/auth/status') return json({ needs_setup: false, local_login_enabled: true });
    if (path === '/auth/me') return json({ user: { id: 1, username: role, role }, csrf_token: 'fixture' });
    if (path === '/instances') return json({ instances: [detail('alpha'), detail('beta')] });
    if (/^\/instances\/(alpha|beta)$/.test(path)) return json({ detail: detail(path.split('/')[2]) });
    if (path.endsWith('/lifecycle')) return json({ installed: false, process: 'stopped', process_running: false, readiness: 'stopped' });
    if (path.endsWith('/metrics')) return json({ instance_id: path.split('/')[2], resolution: 'raw', interval_seconds: 30, cpu_convention: 'Intercepted client fixture', points: [] });
    if (path.endsWith('/players')) return json({ snapshot: { known: false, players: [], gap: true } });
    if (path.endsWith('/map')) return json({ state: 'ready', discovery: { minecraft_version: '1.21.5', dimensions: ['minecraft:overworld', 'minecraft:the_nether'].map(id => ({ id, kind: 'standard', state: 'ready' })) }, consistency: 'Intercepted saved fixture; independent ages', data_version: 1, sampled_at: now });
    if (path.endsWith('/map/view')) {
      if (holdView) { holdView = false; heldView = true; await new Promise(resolve => { releaseView = resolve; }); }
      const dimension = url.searchParams.get('dimension');
      return json({ sampled_at: now, tiles: [{ chunk_x: Number(url.searchParams.get('chunk_x')), chunk_z: Number(url.searchParams.get('chunk_z')), state: 'missing', cells: [], reason: 'Fixture ' + dimension, sampled_at: now }], native: { snapshot: { known: false }, positions: [], gap: true, reason: 'Fixture coordinates unknown' } });
    }
    if (path.endsWith('/game-events')) {
      if (holdHistory) { holdHistory = false; heldHistory = true; await new Promise(resolve => { releaseHistory = resolve; }); }
      if (failHistory) return route.fulfill({ status: 503, json: { error: { message: 'Fixture history failed', code: 'unavailable' } } });
      if (bulkEvents) {
        const cursor = Number(url.searchParams.get('after'));
        return json({ instance_id: 'alpha', events: Array.from({ length: 100 }, (_, i) => event(cursor + i + 1, cursor + i === 0 ? 'source_gap' : 'death', 'Bulk', now, 'Bulk ' + (cursor + i + 1))), next_cursor: cursor + 100, more: cursor < 500, retention_floor: 1, gap: cursor === 0, reason: '', coverage: { available: true, observed_at: now, logs_observed_at: now, parser_capability: 'static_reviewed_patterns' } });
      }
      const more = url.searchParams.get('after') === '0';
      return json({ instance_id: path.split('/')[2], events: more ? [event(1, 'death', 'Alice', '2026-10-17T10:00:00Z', 'Alice fell'), event(2, 'source_gap', '', now, 'Missing source fixture')] : [event(3, 'advancement', 'Bob', now, 'Bob advanced')], next_cursor: more ? 2 : 3, more, retention_floor: 1, gap: more, reason: more ? 'Fixture source gap for ' + path.split('/')[2] : '', coverage: { available: true, configured_version: '1.21.5', parser_capability: 'static_reviewed_patterns', observed_at: now, logs_observed_at: now, reason: 'Fixture coverage' } });
    }
    if (path.endsWith('/recovery')) return json({ enabled: false, attempts: [] });
    if (path.endsWith('/schedules')) {
      if (route.request().method() === 'POST') { const input = route.request().postDataJSON(); if (['constructor', 'toString', '__proto__', '0 3 * * 7'].includes(input.expression)) return route.fulfill({ status: 400, json: { error: { code: 'validation_failed', message: 'Fixture schedule expression rejected' } } }); mutations.push(input); return json({ ...input, id: 'new' }); }
      return json({ schedules: scheduleRecords, available_actions: ['backup', 'restart', 'command'], default_timezone: 'Europe/Stockholm', time_policy: 'Cron DST fixture policy' });
    }
    return route.fulfill({ status: 500, json: { error: { message: 'Unexpected fixture ' + path } } });
  });
  if (process.env.TASK31C_SCHEDULES_ONLY !== '1') {
  await page.goto(base + '/instances/alpha/map');
  await page.getByRole('checkbox', { name: 'Enable touch map dragging' }).waitFor();
  const map = page.getByRole('img', { name: /^Saved terrain map/ });
  const center = (x, z) => page.getByText(new RegExp(`Center chunk ${x}, ${z} ·`));
  await center(0, 0).waitFor();
  await map.scrollIntoViewIfNeeded(); let box = await map.boundingBox();
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2); await page.mouse.down(); await page.mouse.move(box.x + box.width / 2 + box.width / 3, box.y + box.height / 2); await page.mouse.up();
  await center(-1, 0).waitFor();
  await page.mouse.wheel(0, -100); await page.getByRole('combobox', { name: 'Map zoom', exact: true }).evaluate(el => { if (el.value !== '2× zoom') throw Error('wheel zoom failed'); });
  box = await map.boundingBox(); await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2); await page.mouse.down(); await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2 + box.height * 2 / 3); await page.mouse.up();
  await center(-1, -1).waitFor();
  await map.focus(); await page.keyboard.press('ArrowRight'); await center(0, -1).waitFor();
  await page.keyboard.press('-');
  await page.getByRole('combobox', { name: 'Saved dimension' }).click(); await page.getByRole('option', { name: /minecraft:the_nether/ }).click();
  await center(0, 0).waitFor();
  assert.equal(await page.getByRole('combobox', { name: 'Map zoom', exact: true }).inputValue(), '1× zoom');
  await page.screenshot({ path: '/home/thim/.cache/dilla-tmp/task31c-map-mobile.png', fullPage: true });
  results.push('390px CSS-scaled pointer drag, zoom mapping, keyboard pan and dimension reset');
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
  assert.equal(await map.evaluate(el => getComputedStyle(el).touchAction), 'pan-y');
  await page.getByRole('checkbox', { name: 'Enable touch map dragging' }).check();
  assert.equal(await map.evaluate(el => getComputedStyle(el).touchAction), 'none');
  await page.getByRole('checkbox', { name: 'Enable touch map dragging' }).uncheck();
  await page.setViewportSize({ width: 1440, height: 1000 });
  await map.scrollIntoViewIfNeeded(); box = await map.boundingBox(); assert(box.width > 390);
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2); await page.mouse.down(); await page.mouse.move(box.x + box.width / 2 + box.width / 3, box.y + box.height / 2); await page.mouse.up();
  await center(-1, 0).waitFor();
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2); await page.mouse.down(); await page.mouse.move(box.x + box.width / 2 + box.width / 3, box.y + box.height / 2);
  await map.dispatchEvent('pointercancel', { pointerId: 1 }); await page.mouse.up(); await center(-1, 0).waitFor();
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2); await page.mouse.down();
  await map.dispatchEvent('pointerup', { pointerId: 1, clientX: -1e12, clientY: 1e12 }); await page.mouse.up();
  await center(1874998, -1874999).waitFor();
  await page.getByRole('button', { name: 'East', exact: true }).click(); await center(1874998, -1874999).waitFor();
  holdView = true;
  await page.getByRole('button', { name: 'Retry / refresh' }).click();
  for (let i = 0; i < 50 && !heldView; i++) await page.waitForTimeout(20);
  assert(heldView);
  await page.getByRole('combobox', { name: 'Saved dimension' }).click(); await page.getByRole('option', { name: /minecraft:overworld/ }).click();
  await center(0, 0).waitFor(); releaseView();
  await page.getByText(/Fixture minecraft:overworld/).waitFor({ state: 'attached' });
  assert.equal(await page.getByText(/Fixture minecraft:the_nether/).count(), 0);
  heldView = false; holdView = true; await page.getByRole('button', { name: 'Retry / refresh' }).click();
  for (let i = 0; i < 50 && !heldView; i++) await page.waitForTimeout(20);
  assert(heldView);
  await page.evaluate(() => { history.pushState(null, '', '/instances/beta/map'); dispatchEvent(new PopStateEvent('popstate')); });
  await center(0, 0).waitFor(); releaseView();
  await page.getByText(/Fixture minecraft:overworld/).waitFor({ state: 'attached' });
  await map.focus(); await page.keyboard.press('+'); await page.keyboard.press('ArrowDown'); await center(0, 1).waitFor();
  await page.screenshot({ path: '/home/thim/.cache/dilla-tmp/task31c-map-desktop.png', fullPage: true });
  results.push('desktop CSS scaling, pointer cancellation, world-bound clamping, dimension and instance stale fences, touch opt-in');
  await page.goto(base + '/instances/alpha/game-events');
  await page.getByRole('textbox', { name: 'Player name contains' }).fill('Alice');
  await page.getByText(/Showing 1 of 2 loaded observations/).waitFor();
  await page.getByRole('textbox', { name: 'Event text contains' }).fill('nothing');
  await page.getByText('No loaded observations match these filters.').waitFor();
  await page.getByText(/Loaded source coverage has gaps/).waitFor();
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
  await page.screenshot({ path: '/home/thim/.cache/dilla-tmp/task31c-events-filters.png', fullPage: true });
  await page.getByRole('button', { name: 'Reset event filters' }).click();
  await page.getByRole('button', { name: 'Load more retained observations' }).click();
  await page.getByText(/Showing 3 of 3 loaded observations/).waitFor();
  await page.getByText(/Loaded source coverage has gaps/).waitFor();
  await page.getByRole('combobox', { name: 'Event type' }).click(); await page.getByRole('option', { name: 'Advancements', exact: true }).click();
  await page.getByText(/Showing 1 of 3 loaded observations/).waitFor();
  await page.getByLabel('Observed at or before (UTC)', { exact: true }).fill('2026-10-17T11:00');
  await page.getByText('No loaded observations match these filters.').waitFor();
  await page.getByLabel('Observed at or before (UTC)', { exact: true }).fill('');
  await page.getByLabel('Observed at or after (UTC)', { exact: true }).fill('2026-10-17T13:00');
  await page.getByText('No loaded observations match these filters.').waitFor();
  failHistory = true; await page.getByRole('button', { name: 'Refresh history' }).click(); await page.getByRole('alert').filter({ hasText: 'Fixture history failed' }).waitFor();
  assert(requests.includes('/instances/alpha/game-events?limit=100&after=2'));
  results.push('loaded player/text/type/UTC time filters, pagination, persistent gaps, stale error');
  failHistory = false; bulkEvents = true;
  await page.getByRole('button', { name: 'Reset event filters' }).click();
  await page.getByRole('button', { name: 'Refresh history' }).click();
  await page.getByText(/Showing 100 of 100 loaded observations/).waitFor();
  for (let i = 0; i < 5; i++) {
    await page.getByRole('button', { name: 'Load more retained observations' }).click();
    await page.getByText(new RegExp(`Showing ${Math.min(500, (i + 2) * 100)} of ${Math.min(500, (i + 2) * 100)} loaded observations`)).waitFor();
  }
  await page.getByText(/500-observation screen limit discarded/).waitFor();
  await page.getByText(/Loaded source coverage has gaps/).waitFor();
  assert.equal(await page.getByText('Bulk 1', { exact: true }).count(), 0);
  results.push('500-row bounded buffer, older screen eviction disclosed and source gap retained after eviction');
  bulkEvents = false; holdHistory = true;
  await page.getByRole('button', { name: 'Refresh history' }).click();
  for (let i = 0; i < 50 && !heldHistory; i++) await page.waitForTimeout(20);
  assert(heldHistory);
  await page.evaluate(() => { history.pushState(null, '', '/instances/beta/game-events'); dispatchEvent(new PopStateEvent('popstate')); });
  await page.getByText('History source: Fixture source gap for beta', { exact: true }).waitFor();
  releaseHistory();
  await page.getByText('History source: Fixture source gap for beta', { exact: true }).waitFor();
  assert.equal(await page.getByText('History source: Fixture source gap for alpha', { exact: true }).count(), 0);
  assert.equal(await page.getByRole('textbox', { name: 'Player name contains' }).inputValue(), '');
  results.push('event instance switch aborts old history and resets local filters');
  }
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(base + '/instances/alpha/schedules');
  await page.getByRole('button', { name: 'New schedule' }).click();
  await page.getByRole('textbox', { name: 'Name', exact: true }).fill('Fixture backup');
  await page.getByRole('combobox', { name: 'Timing preset' }).click(); await page.getByRole('option', { name: 'Every six hours (elapsed)', exact: true }).click();
  assert.equal(await page.getByRole('textbox', { name: 'Cron or interval', exact: true }).inputValue(), '@every 6h');
  await page.getByRole('combobox', { name: 'Timing preset' }).click(); await page.getByRole('option', { name: 'Custom cron or interval', exact: true }).click();
  assert.equal(await page.getByRole('combobox', { name: 'Timing preset' }).inputValue(), 'Custom cron or interval');
  await page.getByText(/Every 6 hours of elapsed time/).waitFor();
  for (const expression of ['constructor', 'toString', '__proto__', '0 3 * * 7']) {
    await page.getByRole('textbox', { name: 'Cron or interval', exact: true }).fill(expression);
    await page.getByText(`Timing preview: Custom expression “${expression}” in Europe/Stockholm. The server validates this expression and reports the next run after saving.`, { exact: true }).waitFor();
    assert.equal(await page.getByRole('textbox', { name: 'Cron or interval', exact: true }).inputValue(), expression);
    assert.deepEqual(errors, [], `custom expression ${expression} must not crash the editor`);
  }
  await page.getByRole('button', { name: 'Save disabled schedule', exact: true }).click();
  await page.getByRole('alert').filter({ hasText: 'Fixture schedule expression rejected' }).waitFor();
  assert.equal(await page.getByRole('dialog').isVisible(), true);
  assert.deepEqual(mutations, [], 'server rejects invalid custom text without a successful save');
  results.push('prototype-key expressions remain editable custom text; weekday 7 is custom; server rejection leaves editor usable');
  await page.getByRole('textbox', { name: 'Cron or interval', exact: true }).fill('30 4 * * *');
  await page.getByRole('textbox', { name: 'IANA timezone', exact: true }).fill('UTC');
  await page.getByText(/Daily at 04:30 in UTC/).waitFor();
  await page.getByRole('combobox', { name: 'Action', exact: true }).click(); await page.getByRole('option', { name: 'Restart with countdown', exact: true }).click();
  assert.equal(await page.getByLabel(/^Countdown in seconds/).inputValue(), '300');
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
  await page.screenshot({ path: '/home/thim/.cache/dilla-tmp/task31c-schedule-mobile.png', fullPage: true });
  await page.getByRole('button', { name: 'Save disabled schedule', exact: true }).click();
  assert.deepEqual(mutations, [{ name: 'Fixture backup', enabled: false, action: 'restart', expression: '30 4 * * *', timezone: 'UTC', countdown_seconds: 300 }]);
  scheduleRecords = [{ id: 'other-owner', actor_id: 2, name: 'Another operator schedule', enabled: false, expression: '0 3 * * *', timezone: 'UTC', action: 'restart', countdown_seconds: 300 }];
  role = 'operator'; await page.reload();
  await page.getByText('Another operator schedule', { exact: true }).waitFor();
  assert.equal(await page.getByRole('button', { name: 'New schedule', exact: true }).count(), 1);
  assert.equal(await page.getByRole('button', { name: 'Edit', exact: true }).count(), 0);
  assert.equal(await page.getByRole('button', { name: 'Enable', exact: true }).count(), 0);
  role = 'viewer'; await page.reload(); await page.getByRole('heading', { name: 'Schedules', exact: true }).waitFor(); assert.equal(await page.getByRole('button', { name: 'New schedule' }).count(), 0);
  assert.deepEqual(errors, []);
  results.push('presets, manual expression, timezone preview, five-minute restart default, disabled mutation and viewer gate');
  console.log(JSON.stringify({ evidence: 'intercepted client behavior only', results }, null, 2));
} finally { await browser.close(); }

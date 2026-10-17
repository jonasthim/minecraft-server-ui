// Intercepted shipping UI regressions only; no game or first-user qualification.
// npm --prefix web run dev -- --host 127.0.0.1 --port 15331 --strictPort
// node web/tests/daily-workflows.mjs
import assert from 'node:assert/strict';
import { chromium } from 'playwright-core';
const base = process.env.DAILY_WORKFLOWS_URL || 'http://127.0.0.1:15331';
const browser = await chromium.launch({ headless: true });
const results = [];
try {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const page = await context.newPage(); page.setDefaultTimeout(5000); page.setDefaultNavigationTimeout(10000); const errors = []; page.on('pageerror', error => errors.push(error.message));
  let releaseJobs; const jobsGate = new Promise(resolve => { releaseJobs = resolve; }); let holdJobs = true;
  let role = 'admin'; let denied = false; let failAudit = false; const calls = []; const mutations = [];
  const now = '2026-10-17T12:00:00Z';
  const job = (id, state = 'succeeded', instance = 'alpha') => ({ id, instance_id: instance, kind: 'backup', state, phase: 'Saved', progress: 100, created_at: now, requested_by: 1, cancellable: true, cancel_requested: false });
  const capability = { available: true, method: 'server_property', reason: 'Intercepted fixture' };
  const detail = id => ({ instance: { id, name: id, flavor: 'vanilla', minecraft_version: '1.21.5', java_major: 21, java_runtime_id: '', memory_mib: 2048, game_port: id === 'alpha' ? 25565 : 25566, created_at: now, updated_at: now }, config: { motd: 'fixture', difficulty: 'normal', game_mode: 'survival', max_players: 20, online_mode: true, pvp: true, view_distance: 10, simulation_distance: 10, level_name: 'world', level_seed: '', extra: {} }, eula: { accepted: false }, provisioning: 'not_installed', settings: { legacy_numeric_enums: false, simulation_distance: capability, pvp: capability, desired_runtime: [], seed_reason: 'fixture' } });
  const alice = { uuid: 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee', name: 'Alice', authority: 'server_uuid', source: 'native observation' };
  const bob = { name: 'Bob', authority: 'name_only', source: 'legacy log' };
  await page.route('**/api/v1/**', async route => {
    const url = new URL(route.request().url()); const path = url.pathname.replace('/api/v1', ''); calls.push(path + url.search);
    const json = body => route.fulfill({ json: body });
    const fail = (code, message, status = 403) => route.fulfill({ status, json: { error: { code, message } } });
    if (path === '/auth/status') return json({ needs_setup: false, local_login_enabled: true, oidc_enabled: false });
    if (path === '/auth/me') return json({ user: { id: 1, username: role, display_name: role, role }, csrf_token: 'fixture' });
    if (path === '/auth/logout') { role = 'viewer'; return route.fulfill({ status: 204 }); }
    if (path === '/instances') return json({ instances: [detail('alpha'), detail('beta')] });
    if (/^\/instances\/(alpha|beta)$/.test(path)) return json({ detail: detail(path.split('/')[2]) });
    if (path.endsWith('/lifecycle')) return json({ installed: false, process: 'stopped', process_running: false, readiness: 'stopped', autostart_desired: false, autostart_applied: false });
    if (path.endsWith('/metrics')) return json({ instance_id: 'alpha', resolution: 'raw', interval_seconds: 30, cpu_convention: 'Fixture', points: [] });
    if (path.endsWith('/players')) return json({ snapshot: { instance_id: path.split('/')[2], generation: 'fixture', observed_at: now, known: true, complete: true, count: 3, players: path.includes('alpha') ? [alice, bob, { name: 'Untrusted', authority: 'unknown', source: 'unresolved' }] : [], gap: false, reason: 'Fixture roster' } });
    if (path.includes('/players/lists/')) return json({ list: { kind: 'whitelist', entries: [], observed_at: now, runtime_verified: false } });
    if (path.includes('/players/history')) return json({ sessions: [{ id: 1, player: bob, first_seen: now, last_seen: now, gap: false }], events: [], next_cursor: 1, next_event_cursor: 0, gap: false, event_gap: false, reason: 'Fixture history' });
    if (path.endsWith('/players/mutations')) { mutations.push(JSON.parse(route.request().postData())); return json({ job: job('mutation') }); }
    if (path === '/jobs') { if (holdJobs) await jobsGate; return json({ jobs: [job('recent', 'failed'), job('other', 'succeeded', 'beta')] }); }
    if (path.endsWith('/events')) return route.fulfill({ contentType: 'text/event-stream', body: '' });
    if (path.endsWith('/logs')) return json({ logs: [{ id: 1, job_id: 'old', created_at: now, message: 'redacted saved output' }] });
    if (path.startsWith('/jobs/')) { if (denied) return fail('forbidden', 'Job access denied by backend'); return json({ job: job(decodeURIComponent(path.slice(6))) }); }
    if (path === '/audit') {
      if (failAudit) return fail('unavailable', 'Audit temporarily unavailable', 503);
      if (url.searchParams.has('before')) return json({ entries: [{ id: 1, ts: now, action: 'old_record', details: {} }] });
      return json({ entries: Array.from({ length: 100 }, (_, index) => ({ id: 200 - index, ts: now, username: 'admin', action: index ? 'account.changed' : 'configuration.changed', instance_id: 'alpha', details: index ? {} : { before: '<img src=x onerror="window.auditInjected=true">', after: '[REDACTED]' } })) });
    }
    return fail('unexpected_fixture_request', 'Unexpected fixture request ' + path, 500);
  });
  await page.goto(base + '/jobs?job=outside%20recent');
  await page.getByRole('dialog').waitFor();
  await page.getByRole('button', { name: 'Activity · loading…', exact: true, includeHidden: true }).waitFor({ state: 'attached' });
  holdJobs = false; releaseJobs();
  await page.getByText('outside recent', { exact: true }).waitFor();
  await page.getByText('Job finished; saved output loaded', { exact: true }).waitFor();
  assert(calls.includes('/jobs/outside%20recent'), 'exact job must load outside recent results');
  assert(!calls.some(call => call.includes('/events')), 'terminal job must not open SSE');
  await page.keyboard.press('Escape');
  await page.getByRole('combobox', { name: 'Job state', exact: true }).click(); await page.getByRole('option', { name: 'failed', exact: true }).click();
  await page.getByText(/Showing 1 of 2 loaded recent jobs/).waitFor();
  await page.getByRole('textbox', { name: 'Exact instance ID', exact: true }).fill('beta');
  await page.getByText('No loaded jobs match these filters.', { exact: true }).waitFor();
  assert.equal(await page.getByText('No jobs yet.', { exact: false }).count(), 0);
  denied = true;
  await page.goto(base + '/jobs?job=forbidden');
  await page.getByRole('alert').filter({ hasText: 'Job access denied by backend' }).waitFor();
  assert.equal(await page.getByRole('button', { name: 'Cancel job', exact: true }).count(), 0);
  denied = false;
  results.push('exact out-of-list job, terminal no-SSE, state/instance loaded-scope filters, backend authorization failure');
  await page.keyboard.press('Escape');
  await page.getByRole('button', { name: 'Open navigation', exact: true }).click();
  await page.getByRole('button', { name: 'Activity · 0 active', exact: true }).click();
  await page.getByText('Up to 100 recent authorized jobs.', { exact: false }).waitFor();
  await page.getByRole('button', { name: 'backup · alpha · failed', exact: true }).click();
  await page.getByText('recent', { exact: true }).waitFor();
  assert.equal(await page.getByRole('link', { name: 'Open jobs page', exact: true }).getAttribute('href'), '/jobs?job=recent');
  await page.keyboard.press('Escape');
  results.push('global activity initially loading, mobile drawer contextual detail, exact selected-job link');
  await page.goto(base + '/audit');
  const details = page.locator('summary').filter({ hasText: 'Recorded details for event 200' }); await details.waitFor(); await details.focus(); await page.keyboard.press('Enter');
  await page.getByText('<img src=x onerror=', { exact: false }).waitFor();
  assert.equal(await page.locator('main img').count(), 0); assert.equal(await page.evaluate(() => window.auditInjected), undefined);
  await page.getByRole('textbox', { name: 'Filter loaded audit history', exact: true }).fill('old_record');
  await page.getByText('No loaded audit events match this filter.', { exact: true }).waitFor();
  await page.getByRole('button', { name: 'Load older events', exact: true }).click();
  await page.getByText('old_record', { exact: true }).waitFor();
  assert(calls.includes('/audit?before=101'));
  await page.getByText('End of retained audit history at the last read.', { exact: true }).waitFor();
  failAudit = true; await page.getByRole('button', { name: 'Refresh audit history', exact: true }).click();
  await page.getByRole('alert').filter({ hasText: 'Audit temporarily unavailable' }).waitFor();
  assert.equal(await page.getByText('No audit events yet.', { exact: true }).count(), 0);
  results.push('escaped redacted audit detail via keyboard, truthful local filtering, before-cursor paging, failed refresh retains stale scope');
  await page.goto(base + '/instances/alpha/players');
  await page.getByRole('button', { name: 'Use Alice in player form', exact: true }).click();
  assert.equal(await page.getByRole('textbox', { name: 'Exact username', exact: true }).inputValue(), 'Alice');
  assert.equal(await page.getByRole('textbox', { name: 'Instance-local player UUID', exact: true }).inputValue(), alice.uuid);
  await page.getByRole('button', { name: 'Use Bob in player form', exact: true }).last().click();
  assert.equal(await page.getByRole('textbox', { name: 'Instance-local player UUID', exact: true }).inputValue(), '', 'name-only prefill must clear previous UUID');
  assert.equal(await page.getByRole('button', { name: 'Use Untrusted in player form', exact: true }).isDisabled(), true);
  await page.getByRole('button', { name: 'Queue player operation', exact: true }).click();
  await page.getByRole('link', { name: 'Open jobs and output', exact: true }).waitFor();
  assert.deepEqual(mutations, [{ action: 'kick', mode: 'online', name: 'Bob' }]);
  assert.equal(await page.getByRole('link', { name: 'Open jobs and output', exact: true }).getAttribute('href'), '/jobs?job=mutation');
  await page.evaluate(() => { history.pushState(null, '', '/instances/beta/players'); dispatchEvent(new PopStateEvent('popstate')); });
  await page.getByRole('textbox', { name: 'Exact username', exact: true }).waitFor();
  assert.equal(await page.getByRole('textbox', { name: 'Exact username', exact: true }).inputValue(), '');
  assert.equal(await page.getByRole('link', { name: 'Open jobs and output', exact: true }).count(), 0);
  await page.getByRole('button', { name: 'Sign out', exact: true, includeHidden: true }).evaluate(button => button.click());
  await page.getByRole('heading', { name: 'Players', exact: true }).waitFor();
  assert.equal(await page.getByRole('button', { name: 'Queue player operation', exact: true }).count(), 0);
  assert.equal(await page.getByRole('button', { name: /^Use .* in player form$/ }).count(), 0);
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
  assert.deepEqual(errors, []);
  results.push('observed UUID/name prefill, name-only clears UUID, unknown refusal, exact operation link, instance/role clears actions, 390px no overflow');
  await context.close();
  console.log(JSON.stringify({ evidence: 'intercepted shipping UI only', results }, null, 2));
} finally { await browser.close(); }

// Isolated Vite + intercepted API evidence only. No publisher, native updater,
// installed service, production trust, Minecraft, or user preview is exercised.
// TASK32A_URL=http://127.0.0.1:15174 node web/tests/selfupdate-discovery.mjs
import assert from 'node:assert/strict';
import { chromium } from 'playwright-core';
import { mkdir, writeFile } from 'node:fs/promises';

const base = process.env.TASK32A_URL ?? 'http://127.0.0.1:15174';
const shots = process.env.TASK32A_SCREENSHOTS ?? '/home/thim/.cache/dilla-tmp/task32a-browser';
await mkdir(shots, { recursive: true });
const browser = await chromium.launch({ headless: true });
const results = [];
const hash = 'a'.repeat(64);
const stage = { id: 'b'.repeat(64), version: 'v1.10.0', platform: 'linux/amd64', schema: 16, binary_sha256: hash, created_at: '2026-10-17T12:00:00Z', expires_at: '2026-11-01T00:00:00Z' };
const check = { ...stage, published_at: '2026-10-17T12:00:00Z' };
const release = { version: 'v1.10.0', title: 'Manager release', notes: '<img src=x onerror="window.injected=true">\n**Literal release notes**', notes_truncated: true, published_at: '2026-10-16T12:00:00Z', url: 'https://github.com/example/manager/releases/tag/v1.10.0' };

async function scenario(name, options, test) {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  const errors = []; const calls = { discover: 0, check: 0, stage: 0, apply: 0, status: 0 };
  const fixture = { current: 'v1.9.0', stages: options.staged ? [stage] : [], disconnect: false, ...options };
  page.on('pageerror', error => errors.push(error.message));
  await page.route('**/api/v1/**', async route => {
    const path = new URL(route.request().url()).pathname.replace('/api/v1', '');
    const json = body => route.fulfill({ json: body });
    const reject = message => route.fulfill({ status: 503, json: { error: { code: 'fixture_rejected', message } } });
    if (path === '/auth/status') return json({ needs_setup: false, local_login_enabled: true, oidc_enabled: false });
    if (path === '/auth/me') return json({ user: { id: 1, username: 'admin', display_name: 'Admin', email: '', role: fixture.role ?? 'admin', disabled: false, local: true }, csrf_token: 'fixture' });
    if (path === '/instances') return json({ instances: [] });
    if (path === '/jobs') return json({ jobs: [] });
    if (path === '/system/update') {
      calls.status++;
      if (fixture.disconnect) return route.abort('connectionrefused');
      return json({ current_version: fixture.current, schema: 16, trust_configured: fixture.trust !== false, activation_available: fixture.trust !== false, disabled_reason: fixture.trust === false ? 'Release trust is not configured.' : '', schema_policy: 'unchanged_schema_only', stages: fixture.stages, staging_error: '' });
    }
    if (path === '/system/update/discover') {
      calls.discover++; assert.equal(route.request().method(), 'POST');
      if (fixture.holdDiscovery) await fixture.holdDiscovery;
      if (fixture.discoveryError) return route.abort('failed');
      return json({ current_version: fixture.current, checked_at: '2026-10-17T12:00:00Z', cached: fixture.cached ?? false, comparison: fixture.comparison ?? 'newer', release: fixture.comparison === 'no_release' ? null : release });
    }
    if (path === '/system/update/check') { calls.check++; assert.equal(route.request().postDataJSON().version, fixture.expectedVersion ?? 'v1.10.0'); return json(check); }
    if (path === '/system/update/stage') {
      calls.stage++;
      if (fixture.rejectStage) return reject('Signed download rejected');
      fixture.stages = [stage];
      if (fixture.loseStageResponse) return route.abort('failed');
      return json(stage);
    }
    if (path === '/system/update/apply') {
      calls.apply++; assert.deepEqual(route.request().postDataJSON(), { stage_id: stage.id, confirm: true });
      if (fixture.rejectApply) return reject('Native activation was refused');
      fixture.disconnect = true;
      if (fixture.holdApply) await fixture.holdApply;
      if (fixture.loseApplyResponse) return route.abort('failed');
      return route.fulfill({ status: 204 });
    }
    throw new Error('Unexpected intercepted API: ' + path);
  });
  await page.clock.install();
  try {
    await page.goto(base + '/settings/update');
    await test({ page, fixture, calls });
    assert.deepEqual(errors, []);
    results.push({ name, calls, errors });
  } finally { await context.close(); }
}

try {
  await scenario('plain notes, explicit selection and signed verification, keyboard, 390px', {}, async ({ page, calls }) => {
    const discover = page.getByRole('button', { name: 'Check latest stable release', exact: true });
    await discover.waitFor({ timeout: 1500 }); await discover.focus(); await page.keyboard.press('Enter');
    await page.getByText('A newer stable release is available.', { exact: true }).waitFor();
    await page.getByText(release.notes, { exact: true }).waitFor();
    await page.getByText('Release notes were truncated.', { exact: true }).waitFor();
    await page.getByText(/Checked .*Fresh publisher observation/).waitFor();
    assert.equal(await page.locator('main img').count(), 0);
    assert.equal(await page.evaluate(() => window.injected), undefined);
    assert.equal(await page.getByRole('link', { name: 'View publisher release' }).getAttribute('href'), release.url);
    await page.getByLabel('Release version', { exact: true }).fill('v2.0.0');
    // A selected signed check must be discarded when using discovered metadata.
    await page.getByRole('button', { name: 'Use this version', exact: true }).click();
    assert.equal(await page.getByLabel('Release version', { exact: true }).inputValue(), 'v1.10.0');
    assert.equal(calls.check, 0); assert.equal(calls.stage, 0); assert.equal(calls.apply, 0);
    assert.equal(await page.getByRole('button', { name: 'Download and verify release' }).count(), 0);
    await page.getByRole('button', { name: 'Verify release metadata' }).click();
    await page.getByRole('button', { name: 'Download and verify release' }).waitFor();
    assert.equal(calls.check, 1); assert.equal(calls.stage, 0);
    await page.getByRole('button', { name: 'Use this version', exact: true }).click();
    assert.equal(await page.getByRole('button', { name: 'Download and verify release' }).count(), 0);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    await page.evaluate(() => { document.activeElement?.blur(); scrollTo(0, 0); });
    await page.screenshot({ path: shots + '/discovery-390.png', fullPage: true });
  });
  await scenario('network error retries explicitly and empty result is successful', { discoveryError: true }, async ({ page, fixture, calls }) => {
    await page.getByRole('button', { name: 'Check latest stable release', exact: true }).click();
    await page.getByRole('alert').filter({ hasText: 'Latest release check failed' }).waitFor();
    assert.equal(calls.discover, 1);
    await page.clock.runFor(10000); assert.equal(calls.discover, 1);
    fixture.discoveryError = false; fixture.comparison = 'no_release'; fixture.cached = true;
    await page.getByRole('button', { name: 'Retry latest release check', exact: true }).click();
    await page.getByText('No published stable release was found for the configured publisher.', { exact: true }).waitFor();
    await page.getByText(/Cached publisher observation/).waitFor();
    assert.equal(calls.discover, 2);
  });
  for (const [comparison, text] of [['same', 'The publisher release matches the installed stable version.'], ['older', 'The publisher release is older than the installed stable version.'], ['unknown', 'The installed version cannot be compared with a stable release.']]) {
    await scenario('truthful ' + comparison, { comparison, current: comparison === 'unknown' ? '0.0.0-dev' : 'v1.10.0' }, async ({ page }) => {
      await page.getByRole('button', { name: 'Check latest stable release', exact: true }).click();
      await page.getByText(text, { exact: true }).waitFor();
    });
  }
  await scenario('discovery cancellation ignores stale response and survives navigation', {}, async ({ page, fixture }) => {
    let releaseHeld;
    fixture.holdDiscovery = new Promise(resolve => { releaseHeld = resolve; });
    await page.getByRole('button', { name: 'Check latest stable release', exact: true }).click();
    await page.getByRole('button', { name: 'Cancel release check', exact: true }).click();
    await page.getByLabel('Release version', { exact: true }).fill('v2.0.0');
    releaseHeld(); await page.clock.runFor(100);
    assert.equal(await page.getByText(release.title, { exact: true }).count(), 0);
    assert.equal(await page.getByLabel('Release version', { exact: true }).inputValue(), 'v2.0.0');
    fixture.holdDiscovery = new Promise(resolve => { releaseHeld = resolve; });
    const pending = page.waitForRequest('**/system/update/discover');
    await page.getByRole('button', { name: 'Check latest stable release', exact: true }).click();
    await pending;
    await page.getByRole('button', { name: 'Open navigation' }).click();
    await page.getByRole('link', { name: 'Jobs', exact: true }).click();
    await page.getByRole('heading', { name: 'Jobs', exact: true }).waitFor();
    releaseHeld(); await page.clock.runFor(100);
    assert.equal(await page.getByText(release.title, { exact: true }).count(), 0);
  });
  await scenario('lost stage response recovery and confirmation invalidation', { loseStageResponse: true }, async ({ page, calls }) => {
    await page.getByLabel('Release version', { exact: true }).fill('v1.10.0');
    await page.getByRole('button', { name: 'Verify release metadata' }).click();
    await page.getByRole('button', { name: 'Download and verify release' }).click();
    await page.getByText(/staging response was lost.*recovered/).waitFor();
    assert.equal(await page.getByRole('checkbox', { name: /I confirm/ }).isChecked(), false);
    await page.getByRole('checkbox', { name: /I confirm/ }).check();
    await page.getByRole('button', { name: 'Check latest stable release', exact: true }).click();
    await page.getByRole('button', { name: 'Use this version', exact: true }).click();
    assert.equal(await page.getByRole('button', { name: 'Activate manager update' }).count(), 0);
    assert.equal(calls.apply, 0);
    await page.reload(); await page.getByRole('checkbox', { name: /I confirm/ }).waitFor();
    assert.equal(await page.getByRole('checkbox', { name: /I confirm/ }).isChecked(), false);
    assert.equal(calls.stage, 1);
  });
  for (const loseApplyResponse of [false, true]) {
    await scenario('activation reconnect, no replay, lost response=' + loseApplyResponse, { staged: true, loseApplyResponse }, async ({ page, fixture, calls }) => {
      await page.getByRole('checkbox', { name: /I confirm/ }).check();
      await page.getByRole('button', { name: 'Activate manager update' }).click();
      await page.getByText(/Waiting for the manager to return/).waitFor();
      await page.clock.runFor(6000);
      assert.equal(calls.apply, 1);
      assert.equal(await page.getByText(/Manager is responding with installed version/).count(), 0);
      fixture.disconnect = false; fixture.current = 'v1.10.0'; fixture.stages = [];
      await page.clock.runFor(3000);
      await page.getByText('Manager is responding with installed version v1.10.0.', { exact: true }).waitFor();
      assert.equal(calls.apply, 1);
    });
  }
  await scenario('activation stays old then bounded failure reports actual version', { staged: true }, async ({ page, fixture, calls }) => {
    await page.getByRole('checkbox', { name: /I confirm/ }).check();
    await page.getByRole('button', { name: 'Activate manager update' }).click();
    await page.getByText(/Waiting for the manager to return/).waitFor();
    fixture.disconnect = false;
    await page.clock.runFor(61000);
    await page.getByRole('alert').filter({ hasText: 'Could not verify v1.10.0 within one minute. Last observed running version: v1.9.0.' }).waitFor();
    const reads = calls.status; await page.clock.runFor(10000);
    assert.equal(calls.status, reads); assert.equal(calls.apply, 1);
  });
  await scenario('reconnect can be canceled without activation replay', { staged: true }, async ({ page, calls }) => {
    await page.getByRole('checkbox', { name: /I confirm/ }).check();
    await page.getByRole('button', { name: 'Activate manager update' }).click();
    await page.getByRole('button', { name: 'Stop waiting', exact: true }).click();
    await page.getByText(/Stopped waiting.*Activation outcome remains unverified/).waitFor();
    const reads = calls.status; await page.clock.runFor(10000);
    assert.equal(calls.status, reads); assert.equal(calls.apply, 1);
  });
  await scenario('lost activation acknowledgement is time bounded', { staged: true }, async ({ page, fixture, calls }) => {
    let releaseHeld;
    fixture.holdApply = new Promise(resolve => { releaseHeld = resolve; });
    await page.getByRole('checkbox', { name: /I confirm/ }).check();
    await page.getByRole('button', { name: 'Activate manager update' }).click();
    await page.clock.runFor(11000);
    await page.getByText(/Waiting for the manager to return.*response was lost/).waitFor();
    assert.equal(calls.apply, 1);
    releaseHeld();
    await page.getByRole('button', { name: 'Stop waiting', exact: true }).click();
  });
  await scenario('explicit activation rejection stays visible across refresh', { staged: true, rejectApply: true }, async ({ page, calls }) => {
    await page.getByRole('checkbox', { name: /I confirm/ }).check();
    await page.getByRole('button', { name: 'Activate manager update' }).click();
    await page.getByRole('alert').filter({ hasText: 'Native activation was refused' }).waitFor();
    await page.getByRole('button', { name: 'Refresh status', exact: true }).click();
    await page.getByRole('alert').filter({ hasText: 'Native activation was refused' }).waitFor();
    assert.equal(calls.apply, 1);
  });
  await scenario('empty trust disables discovery', { trust: false }, async ({ page, calls }) => {
    const button = page.getByRole('button', { name: 'Check latest stable release', exact: true });
    await button.waitFor(); assert.equal(await button.isDisabled(), true); assert.equal(calls.discover, 0);
  });
  for (const role of ['viewer', 'operator']) await scenario('role gate ' + role, { role }, async ({ page, calls }) => {
    await page.getByText('Administrator access required.', { exact: true }).waitFor();
    assert.equal(calls.discover, 0); assert.equal(calls.status, 0);
  });
  const report = JSON.stringify({ interceptionOnly: true, results, screenshots: shots }, null, 2);
  await writeFile(shots + '/results.json', report + '\n');
  console.log(report);
} finally { await browser.close(); }

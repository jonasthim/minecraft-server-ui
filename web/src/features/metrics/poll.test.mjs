import assert from 'node:assert/strict';
import { test } from 'node:test';
import { startMetricsPoll } from './poll.ts';
const flush = async () => { await Promise.resolve(); await Promise.resolve(); };
function scenario(t) {
  const document = Object.assign(new EventTarget(), { hidden: false });
  Object.defineProperty(globalThis, 'document', { value: document, configurable: true });
  const timers = new Map(); let next = 0;
  t.mock.method(globalThis, 'setTimeout', callback => { timers.set(++next, callback); return next; });
  t.mock.method(globalThis, 'clearTimeout', id => timers.delete(id));
  const pending = [], values = [], errors = [];
  const load = signal => new Promise((resolve, reject) => pending.push({ signal, resolve, reject }));
  const start = () => startMetricsPoll(load, value => values.push(value), error => errors.push(error));
  const visibility = hidden => { document.hidden = hidden; document.dispatchEvent(new Event('visibilitychange')); };
  return { pending, values, errors, timers, start, visibility };
}
test('late hidden request failure cannot replace a fresh visible result or add polling', async t => {
  const s = scenario(t), stop = s.start();
  s.visibility(true); assert.equal(s.pending[0].signal.aborted, true);
  s.visibility(false); s.pending[1].resolve('fresh roster'); await flush();
  s.pending[0].reject(new Error('obsolete network failure')); await flush();
  assert.deepEqual(s.values, ['fresh roster']); assert.deepEqual(s.errors, []);
  assert.equal(s.timers.size, 1, 'obsolete completion scheduled another poll'); stop();
});
test('range replacement and unmount reject abort-ignoring old data and errors', async t => {
  const s = scenario(t), stopHour = s.start(); stopHour(); const stopWeek = s.start();
  s.pending[1].resolve('week history'); await flush();
  s.pending[0].resolve('old hour history'); await flush(); assert.deepEqual(s.values, ['week history']);
  s.visibility(true); assert.equal(s.timers.size, 0); s.visibility(false);
  stopWeek(); s.pending[2].reject(new Error('unmounted')); await flush();
  assert.deepEqual(s.errors, []); assert.equal(s.timers.size, 0); assert.equal(s.pending[2].signal.aborted, true);
});

import assert from 'node:assert/strict';
import { test } from 'node:test';
import { startVisiblePoll } from './poll.ts';

test('visibility cancels obsolete work and fences an abort-ignoring response', async () => {
  const events = new EventTarget();
  const doc = Object.assign(events, { hidden: false });
  Object.defineProperty(globalThis, 'document', { value: doc, configurable: true });
  const pending = [];
  const received = [];
  const stop = startVisiblePoll(signal => new Promise(resolve => { pending.push({ signal, resolve }); }), value => received.push(value), error => { throw error; });
  assert.equal(pending.length, 1);
  doc.hidden = true; events.dispatchEvent(new Event('visibilitychange'));
  assert.equal(pending[0].signal.aborted, true);
  doc.hidden = false; events.dispatchEvent(new Event('visibilitychange'));
  assert.equal(pending.length, 2);
  pending[1].resolve(2); await Promise.resolve(); await Promise.resolve();
  pending[0].resolve(1); await Promise.resolve(); await Promise.resolve();
  assert.deepEqual(received, [2]);
  stop(); assert.equal(pending[1].signal.aborted, true);
});

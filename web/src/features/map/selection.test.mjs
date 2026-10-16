import assert from 'node:assert/strict';
import { test } from 'node:test';
import { initialSelection, reduceSelection } from './selection.ts';

const summary = (...ids) => ({ discovery: { dimensions: ids.map(id => ({ id })) } });
const savedTerrain = { tiles: [{ dimension: 'example:moon', cells: [{ block: 'minecraft:stone' }] }] };
function observedMoon() {
  let state = reduceSelection(initialSelection, { type: 'summary', value: summary('minecraft:overworld', 'example:moon') });
  state = reduceSelection(state, { type: 'dimension', value: 'example:moon' });
  return reduceSelection(state, { type: 'view', dimension: 'example:moon', value: savedTerrain });
}

test('discovery replacement removes old terrain even when the next request fails or arrives late', () => {
  const state = reduceSelection(observedMoon(), { type: 'summary', value: summary('minecraft:overworld') });
  assert.equal(state.dimension, 'minecraft:overworld');
  assert.equal(state.view, null); // A failed request cannot leave Moon terrain under Overworld.
  const late = reduceSelection(state, { type: 'view', dimension: 'example:moon', value: savedTerrain });
  assert.equal(late.view, null);
});

test('empty discovery clears selection and terrain without resurrecting it on recovery', () => {
  const empty = reduceSelection(observedMoon(), { type: 'summary', value: summary() });
  assert.equal(empty.dimension, '');
  assert.equal(empty.view, null);
  const recovered = reduceSelection(empty, { type: 'summary', value: summary('example:moon') });
  assert.equal(recovered.dimension, 'example:moon');
  assert.equal(recovered.view, null);
});

test('ordinary discovery polls preserve the selected custom dimension and its useful terrain', () => {
  const state = reduceSelection(observedMoon(), { type: 'summary', value: summary('example:new', 'example:moon') });
  assert.equal(state.dimension, 'example:moon');
  assert.equal(state.view, savedTerrain);
});

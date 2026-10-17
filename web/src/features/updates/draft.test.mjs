import { test } from 'node:test';
import assert from 'node:assert/strict';
import { emptyPack, editPackSelection, restorePack, changePackSource } from './draft.ts';
const flags = { optional: true, selected: true, disabled: false, pinned: false, excluded: false };
const preview = { selections: { 'mods/a.jar': flags, 'mods/b.jar': flags }, changes: [{ path: 'config/a.toml', choice: 'keep' }, { path: 'config/b.toml', choice: 'upstream' }, { path: 'mods/a.jar', choice: '' }], source: { selection: { provider: 'modrinth', project_id: 'pack', version_id: 'version', file_id: 'file' } } };
test('cumulative path edits preserve optional defaults, other paths, choices and manual receipts', () => {
 const original = { ...emptyPack(), choices: { 'config/a.toml': 'keep' }, manual: { 'mods/required.jar': 'receipt' }, archive_receipt_id: 'archive' };
 let value = editPackSelection(original, preview, 'mods/a.jar', { selected: false });
 value = editPackSelection(value, preview, 'mods/a.jar', { disabled: true, pinned: true });
 value = editPackSelection(value, preview, 'mods/b.jar', { excluded: true });
 assert.deepEqual(value.selections['mods/a.jar'], { ...flags, selected: false, disabled: true, pinned: true });
 assert.deepEqual(value.selections['mods/b.jar'], { ...flags, excluded: true });
 assert.deepEqual(value.manual, original.manual); assert.deepEqual(value.choices, original.choices); assert.equal(value.archive_receipt_id, 'archive');
 assert.deepEqual(original.selections, {}); assert.equal(flags.selected, true);
});
test('restoring a public preview keeps typed conflict decisions and exact source without inventing private receipts', () => {
 const value = restorePack(preview);
 assert.deepEqual(value.pack, preview.source.selection);
 assert.deepEqual(value.choices, { 'config/a.toml': 'keep', 'config/b.toml': 'upstream' });
 assert.deepEqual(value.selections, preview.selections); assert.deepEqual(value.manual, {}); assert.equal(value.previous_preview_id, undefined);
});
test('editing the publisher source retains user choices while clearing receipts bound to another exact selection', () => {
 const original = { ...restorePack(preview), manual: { 'mods/a.jar': 'old-receipt' }, archive_receipt_id: 'old-archive' };
 const value = changePackSource(original, { file_id: 'other-exact-file' });
 assert.equal(value.pack.file_id, 'other-exact-file'); assert.deepEqual(value.choices, original.choices); assert.deepEqual(value.selections, original.selections); assert.deepEqual(value.manual, {}); assert.equal(value.archive_receipt_id, undefined);
});

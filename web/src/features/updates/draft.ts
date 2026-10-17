import type { components } from '../../api/schema';

type PackRequest = components['schemas']['ModpackRequest'];
type PackPreview = components['schemas']['ModpackPreview'];
export const emptyPack = (): PackRequest => ({ pack: { provider: 'modrinth', project_id: '', version_id: '', file_id: '' }, choices: {}, selections: {}, manual: {} });

/** Merge all path defaults before editing one flag; keep earlier edits intact. */
export function editPackSelection(request: PackRequest, preview: PackPreview, path: string, change: Partial<PackRequest['selections'][string]>): PackRequest {
  return { ...request, selections: { ...preview.selections, ...request.selections, [path]: { ...preview.selections[path], ...request.selections[path], ...change } } };
}

/** Restored public plans carry choices/selections, but omit private receipts. */
export function restorePack(preview: PackPreview): PackRequest {
  const choices: PackRequest['choices'] = {};
  for (const change of preview.changes) if (change.choice === 'keep' || change.choice === 'upstream') choices[change.path] = change.choice;
  return { pack: { ...preview.source.selection }, choices, selections: { ...preview.selections }, manual: {} };
}

export function changePackSource(request: PackRequest, change: Partial<PackRequest['pack']>): PackRequest {
  return { ...request, pack: { ...request.pack, ...change }, manual: {}, archive_receipt_id: undefined, previous_preview_id: undefined };
}

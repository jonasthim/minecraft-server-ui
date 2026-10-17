import type { components } from '../../api/schema';
export type Line = components['schemas']['ConsoleLine'];
export type Page = components['schemas']['ConsolePage'];

// Both HTTP history and SSE are untrusted boundaries. Validate before buffering/exporting.
export function checkedPage(value: Page): Page {
  if (!value || !Array.isArray(value.lines) || value.lines.length > 500 || !Number.isSafeInteger(value.next_cursor) || value.next_cursor < 0 || typeof value.gap !== 'boolean') throw Error('Invalid console history page.');
  let previous = 0;
  for (const line of value.lines) {
    if (!Number.isSafeInteger(line.id) || line.id <= previous || line.id > value.next_cursor || typeof line.message !== 'string' || line.message.length > 8192 || typeof line.gap !== 'boolean' || typeof line.created_at !== 'string' || !Number.isFinite(Date.parse(line.created_at))) throw Error('Invalid console history line.');
    previous = line.id;
  }
  if (value.lines.length && previous !== value.next_cursor) throw Error('Invalid console history cursor.');
  return value;
}
export function mergeLines(previous: Line[], incoming: Line[]): Line[] {
  return [...new Map([...previous, ...incoming].map(line => [line.id, line])).values()].sort((a, b) => a.id - b.id).slice(-500);
}
export function presentation(line: Line) {
  // Labels describe recognisable log tokens; arbitrary server text stays escaped React text.
  if (line.gap) return { label: 'GAP', color: 'var(--mantine-color-yellow-7)' };
  if (/\b(ERROR|FATAL)\b/i.test(line.message)) return { label: 'Error', color: 'var(--mantine-color-red-7)' };
  if (/\bWARN(?:ING)?\b/i.test(line.message)) return { label: 'Warning', color: 'var(--mantine-color-yellow-7)' };
  return { label: 'Output', color: 'var(--mantine-color-gray-6)' };
}

// A generation owns one request and one next timer. Abort is advisory; the
// generation fence also rejects transports that complete after cancellation.
export function startMetricsPoll<T>(load: (signal: AbortSignal) => Promise<T>, receive: (value: T) => void, fail: (error: unknown) => void, interval = 30000) {
  let closed = false, generation = 0;
  let controller: AbortController | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const invalidate = () => { generation++; controller?.abort(); clearTimeout(timer); };
  const run = async () => {
    if (closed || document.hidden) return;
    const current = ++generation;
    const request = new AbortController(); controller = request;
    const active = () => !closed && current === generation && !request.signal.aborted && !document.hidden;
    try { const value = await load(request.signal); if (active()) receive(value); }
    catch (error) { if (active()) fail(error); }
    finally { if (active()) timer = setTimeout(() => { void run(); }, interval); }
  };
  const visibility = () => { invalidate(); if (!document.hidden) void run(); };
  document.addEventListener('visibilitychange', visibility); void run();
  return () => { closed = true; invalidate(); document.removeEventListener('visibilitychange', visibility); };
}

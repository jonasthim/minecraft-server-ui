// One finite request at a time. Visibility and key changes abort obsolete work;
// the generation fence also protects against transports that ignore abort.
export function startVisiblePoll<T>(load: (signal: AbortSignal) => Promise<T>, receive: (value: T) => void, fail: (error: unknown) => void, interval = 5000) {
  let closed = false, generation = 0;
  let controller: AbortController | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const stop = () => { generation++; controller?.abort(); controller = undefined; clearTimeout(timer); };
  const run = async () => {
    if (closed || document.hidden) return;
    const current = ++generation;
    const started = Date.now();
    controller = new AbortController();
    const signal = controller.signal;
    try { const value = await load(signal); if (!closed && !signal.aborted && current === generation) receive(value); }
    catch (error) { if (!closed && !signal.aborted && current === generation) fail(error); }
    finally { if (!closed && current === generation && !document.hidden) timer = setTimeout(() => { void run(); }, Math.max(0, interval - (Date.now() - started))); }
  };
  const visibility = () => { stop(); if (!document.hidden) void run(); };
  document.addEventListener('visibilitychange', visibility);
  void run();
  return () => { closed = true; stop(); document.removeEventListener('visibilitychange', visibility); };
}

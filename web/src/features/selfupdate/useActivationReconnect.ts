import { useEffect, useState } from 'react';
import { api, APIError } from '../../api/client';
import type { components } from '../../api/schema';

type Status = components['schemas']['SelfUpdateStatus'];

// The apply response acknowledges a request. Only a new status observation can
// establish which executable is responding; polling never repeats activation.
export function useActivationReconnect(onStatus: (status: Status) => void) {
  const [target, setTarget] = useState<string | null>(null);
  const [message, setMessage] = useState('');
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    if (!target) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    let lastVersion = '';
    const finish = (text: string, failure: boolean) => {
      setMessage(text); setFailed(failure); setTarget(null);
      controller.abort(); clearTimeout(timer);
    };
    const deadline = setTimeout(() => finish(`Could not verify ${target} within one minute. ${lastVersion ? `Last observed running version: ${lastVersion}.` : 'No running version could be observed.'} Refresh status before deciding whether to retry activation.`, true), 60000);
    const poll = async () => {
      const request = new AbortController();
      const abort = () => request.abort();
      controller.signal.addEventListener('abort', abort, { once: true });
      const timeout = setTimeout(abort, 5000);
      try {
        const value = await api<Status>('/system/update', { signal: request.signal });
        if (controller.signal.aborted) return;
        lastVersion = value.current_version; onStatus(value);
        if (lastVersion === target) {
          finish(`Manager is responding with installed version ${lastVersion}.`, false);
          clearTimeout(deadline); return;
        }
        setMessage(`Waiting for the manager to return with ${target}. Last observed running version: ${lastVersion}.`);
      } catch (error) {
        if (controller.signal.aborted) return;
        if (error instanceof APIError && ['unauthorized', 'forbidden', 'unauthenticated'].includes(error.code)) {
          finish('Manager status access was lost. Sign in again and refresh status to verify the activation outcome.', true);
          clearTimeout(deadline); return;
        }
      } finally {
        clearTimeout(timeout); controller.signal.removeEventListener('abort', abort);
      }
      if (!controller.signal.aborted) timer = setTimeout(() => void poll(), 2000);
    };
    timer = setTimeout(() => void poll(), 1500);
    return () => { controller.abort(); clearTimeout(timer); clearTimeout(deadline); };
  }, [target, onStatus]);

  return {
    waiting: target !== null, message, failed,
    start(version: string, responseLost: boolean) {
      setFailed(false);
      setMessage(`Waiting for the manager to return with ${version}. ${responseLost ? 'The activation response was lost; the outcome is unknown.' : 'The native activation request was accepted.'} Activation is never retried automatically.`);
      setTarget(version);
    },
    cancel() {
      setTarget(null); setFailed(true);
      setMessage('Stopped waiting. Activation outcome remains unverified. Refresh status to inspect the running version.');
    },
  };
}

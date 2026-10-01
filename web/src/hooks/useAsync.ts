import { useCallback, useEffect, useRef, useState } from 'react';

export type AsyncStatus = 'loading' | 'empty' | 'error' | 'ready';

export interface AsyncState<T> {
  data?: T;
  status: AsyncStatus;
  error?: unknown;
  reload: () => void;
}

export interface UseAsyncOptions<T> {
  isEmpty?: (data: T) => boolean;
  onRollback?: (error: unknown) => void;
}

// useAsync drives a page through loading/empty/error states. It ignores results
// from a superseded load so a tenant switch cannot render stale data.
export function useAsync<T>(loader: () => Promise<T>, options: UseAsyncOptions<T> = {}): AsyncState<T> {
  const [data, setData] = useState<T>();
  const [status, setStatus] = useState<AsyncStatus>('loading');
  const [error, setError] = useState<unknown>();
  const [nonce, setNonce] = useState(0);
  const generation = useRef(0);
  const isEmpty = options.isEmpty;
  const onRollback = options.onRollback;

  useEffect(() => {
    const current = ++generation.current;
    setStatus('loading');
    setError(undefined);
    loader()
      .then((value) => {
        if (generation.current !== current) {
          return;
        }
        setData(value);
        setStatus(isEmpty && isEmpty(value) ? 'empty' : 'ready');
      })
      .catch((cause) => {
        if (generation.current !== current) {
          return;
        }
        setError(cause);
        setStatus('error');
        onRollback?.(cause);
      });
  }, [nonce, loader, isEmpty, onRollback]);

  const reload = useCallback(() => setNonce((value) => value + 1), []);
  return { data, status, error, reload };
}

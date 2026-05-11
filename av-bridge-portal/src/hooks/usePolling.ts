"use client";

import { useEffect, useRef, useState, useCallback } from "react";

interface State<T> {
  data: T | null;
  error: Error | null;
  loading: boolean;
  lastUpdated: number | null;
}

export function usePolling<T>(
  fetcher: (signal: AbortSignal) => Promise<T>,
  intervalMs: number,
  deps: ReadonlyArray<unknown> = []
) {
  const [state, setState] = useState<State<T>>({
    data: null,
    error: null,
    loading: true,
    lastUpdated: null,
  });

  const fetcherRef = useRef(fetcher);
  fetcherRef.current = fetcher;

  const abortRef = useRef<AbortController | null>(null);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const tick = useCallback(async () => {
    abortRef.current?.abort();
    const ctrl = new AbortController();
    abortRef.current = ctrl;
    try {
      const data = await fetcherRef.current(ctrl.signal);
      if (!ctrl.signal.aborted) {
        setState({ data, error: null, loading: false, lastUpdated: Date.now() });
      }
    } catch (err) {
      if (!ctrl.signal.aborted) {
        setState((prev) => ({
          ...prev,
          error: err instanceof Error ? err : new Error(String(err)),
          loading: false,
        }));
      }
    }
  }, []);

  // eslint-disable-next-line react-hooks/exhaustive-deps
  const refresh = useCallback(() => tick(), [tick, ...deps]);

  useEffect(() => {
    let stopped = false;
    const loop = async () => {
      while (!stopped) {
        await tick();
        await new Promise<void>((resolve) => {
          timerRef.current = setTimeout(resolve, intervalMs);
        });
      }
    };
    loop();
    return () => {
      stopped = true;
      if (timerRef.current) clearTimeout(timerRef.current);
      abortRef.current?.abort();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [intervalMs, ...deps]);

  return { ...state, refresh };
}

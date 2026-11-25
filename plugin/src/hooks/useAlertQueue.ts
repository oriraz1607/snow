import { useCallback, useEffect, useRef, useState } from 'react';
import { alertApi, ApiClientError } from '../api/client';
import { Alert, ListAlertsQuery, SummaryCounts } from '../types/api';
import { clampPollIntervalSeconds } from '../utils/format';

export interface UseAlertQueueOptions {
  query: ListAlertsQuery;
  pollIntervalSeconds: number;
  autoRefresh: boolean;
}

export interface UseAlertQueueResult {
  items: Alert[];
  summary: SummaryCounts;
  nextCursor: string | null;
  loading: boolean;
  refreshing: boolean;
  lastSyncedAt: Date | null;
  apiDegraded: boolean;
  errorMessage: string | null;
  refresh: () => Promise<void>;
  loadMore: () => Promise<void>;
  upsertAlert: (updated: Alert) => void;
}

const EMPTY_SUMMARY: SummaryCounts = {
  firing: 0,
  acknowledged: 0,
  acknowledged_without_incident: 0,
  dismissed: 0,
  resolved_open_ticket: 0,
};

export function useAlertQueue({
  query,
  pollIntervalSeconds,
  autoRefresh,
}: UseAlertQueueOptions): UseAlertQueueResult {
  const [items, setItems] = useState<Alert[]>([]);
  const [summary, setSummary] = useState<SummaryCounts>(EMPTY_SUMMARY);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [loading, setLoading] = useState<boolean>(true);
  const [refreshing, setRefreshing] = useState<boolean>(false);
  const [lastSyncedAt, setLastSyncedAt] = useState<Date | null>(null);
  const [apiDegraded, setApiDegraded] = useState<boolean>(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  const baseIntervalSec = clampPollIntervalSeconds(pollIntervalSeconds);
  const queryRef = useRef(query);
  queryRef.current = query;

  const activeAbortRef = useRef<AbortController | null>(null);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const backoffSecRef = useRef<number>(baseIntervalSec);
  const mountedRef = useRef<boolean>(true);
  const hasLoadedOnceRef = useRef<boolean>(false);

  const clearScheduledPoll = useCallback(() => {
    if (timerRef.current !== null) {
      clearTimeout(timerRef.current);
      timerRef.current = null;
    }
  }, []);

  const fetchAlerts = useCallback(
    async (isBackgroundPoll: boolean) => {
      if (activeAbortRef.current) {
        activeAbortRef.current.abort();
      }
      const controller = new AbortController();
      activeAbortRef.current = controller;

      if (!hasLoadedOnceRef.current && !isBackgroundPoll) {
        setLoading(true);
      } else {
        setRefreshing(true);
      }

      try {
        const resp = await alertApi.listAlerts(queryRef.current, controller.signal);
        if (!mountedRef.current || controller.signal.aborted) {
          return;
        }
        hasLoadedOnceRef.current = true;
        setItems(resp.items ?? []);
        setSummary(resp.summary ?? EMPTY_SUMMARY);
        setNextCursor(resp.next_cursor ?? null);
        setLastSyncedAt(new Date());
        setApiDegraded(false);
        setErrorMessage(null);
        backoffSecRef.current = baseIntervalSec;
      } catch (err) {
        if (!mountedRef.current || controller.signal.aborted) {
          return;
        }
        if (err instanceof DOMException && err.name === 'AbortError') {
          return;
        }

        const apiErr = err as ApiClientError;
        if (apiErr?.status === 503 || !apiErr?.status) {
          setApiDegraded(true);
          setErrorMessage(apiErr?.message || 'Live updates paused — Alert Manager API unreachable');
          backoffSecRef.current = Math.min(60, Math.max(baseIntervalSec, backoffSecRef.current * 2));
        } else {
          setErrorMessage(apiErr?.message || 'Failed to load alerts.');
        }
      } finally {
        if (mountedRef.current && activeAbortRef.current === controller) {
          activeAbortRef.current = null;
          setLoading(false);
          setRefreshing(false);
        }
      }
    },
    [baseIntervalSec]
  );

  const scheduleNextPoll = useCallback(() => {
    clearScheduledPoll();
    if (!autoRefresh || typeof document !== 'undefined' && document.visibilityState === 'hidden') {
      return;
    }
    const delayMs = backoffSecRef.current * 1000;
    timerRef.current = setTimeout(async () => {
      if (!mountedRef.current) {
        return;
      }
      if (typeof document !== 'undefined' && document.visibilityState === 'hidden') {
        return;
      }
      await fetchAlerts(true);
      if (mountedRef.current) {
        scheduleNextPoll();
      }
    }, delayMs);
  }, [autoRefresh, clearScheduledPoll, fetchAlerts]);

  const queryKey = JSON.stringify(query);

  useEffect(() => {
    mountedRef.current = true;
    backoffSecRef.current = baseIntervalSec;

    void fetchAlerts(false).then(() => {
      if (mountedRef.current) {
        scheduleNextPoll();
      }
    });

    return () => {
      clearScheduledPoll();
      if (activeAbortRef.current) {
        activeAbortRef.current.abort();
        activeAbortRef.current = null;
      }
    };
  }, [queryKey, baseIntervalSec, autoRefresh, fetchAlerts, scheduleNextPoll, clearScheduledPoll]);

  useEffect(() => {
    const handleVisibilityChange = () => {
      if (document.visibilityState === 'hidden') {
        clearScheduledPoll();
        return;
      }
      if (autoRefresh) {
        void fetchAlerts(true).then(() => {
          if (mountedRef.current) {
            scheduleNextPoll();
          }
        });
      }
    };

    document.addEventListener('visibilitychange', handleVisibilityChange);
    return () => {
      document.removeEventListener('visibilitychange', handleVisibilityChange);
    };
  }, [autoRefresh, clearScheduledPoll, fetchAlerts, scheduleNextPoll]);

  useEffect(() => {
    return () => {
      mountedRef.current = false;
      clearScheduledPoll();
      if (activeAbortRef.current) {
        activeAbortRef.current.abort();
      }
    };
  }, [clearScheduledPoll]);

  const refresh = useCallback(async () => {
    clearScheduledPoll();
    await fetchAlerts(false);
    if (mountedRef.current) {
      scheduleNextPoll();
    }
  }, [clearScheduledPoll, fetchAlerts, scheduleNextPoll]);

  const loadMore = useCallback(async () => {
    if (!nextCursor) {
      return;
    }
    setRefreshing(true);
    try {
      const resp = await alertApi.listAlerts({ ...queryRef.current, cursor: nextCursor });
      if (!mountedRef.current) {
        return;
      }
      setItems((prev) => {
        const seen = new Set(prev.map((item) => item.id));
        const appended = (resp.items ?? []).filter((item) => !seen.has(item.id));
        return [...prev, ...appended];
      });
      setSummary(resp.summary ?? EMPTY_SUMMARY);
      setNextCursor(resp.next_cursor ?? null);
      setLastSyncedAt(new Date());
    } catch (err) {
      const apiErr = err as ApiClientError;
      setErrorMessage(apiErr?.message || 'Failed to load next page of alerts.');
    } finally {
      if (mountedRef.current) {
        setRefreshing(false);
      }
    }
  }, [nextCursor]);

  const upsertAlert = useCallback((updated: Alert) => {
    setItems((prev) => prev.map((item) => (item.id === updated.id ? { ...item, ...updated } : item)));
  }, []);

  return {
    items,
    summary,
    nextCursor,
    loading,
    refreshing,
    lastSyncedAt,
    apiDegraded,
    errorMessage,
    refresh,
    loadMore,
    upsertAlert,
  };
}

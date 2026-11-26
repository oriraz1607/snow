import { useCallback, useEffect, useRef, useState } from 'react';
import { alertApi, ApiClientError } from '../api/client';
import { Alert, AlertFilterOptions, ListAlertsQuery, SummaryCounts } from '../types/api';
import { clampPollIntervalSeconds } from '../utils/format';

export interface UseAlertQueueOptions {
  query: ListAlertsQuery;
  pollIntervalSeconds: number;
  autoRefresh: boolean;
}

export interface UseAlertQueueResult {
  items: Alert[];
  summary: SummaryCounts;
  filterOptions: AlertFilterOptions;
  filteredTotal: number;
  nextCursor: string | null;
  pageIndex: number;
  hasNextPage: boolean;
  hasPrevPage: boolean;
  staleOnLaterPage: boolean;
  loading: boolean;
  refreshing: boolean;
  lastSyncedAt: Date | null;
  apiDegraded: boolean;
  errorMessage: string | null;
  refresh: () => Promise<void>;
  goNextPage: () => Promise<void>;
  goPrevPage: () => Promise<void>;
  goFirstPage: () => Promise<void>;
  upsertAlert: (updated: Alert) => void;
}

const EMPTY_SUMMARY: SummaryCounts = {
  firing: 0,
  firing_critical: 0,
  firing_warning: 0,
  acknowledged: 0,
  acknowledged_without_incident: 0,
  non_active_acknowledged: 0,
  dismissed: 0,
  resolved: 0,
  resolved_open_ticket: 0,
  active_closed_ticket: 0,
};

const EMPTY_FILTER_OPTIONS: AlertFilterOptions = {
  environments: [],
  sites: [],
  teams: [],
  instances: [],
};

export function useAlertQueue({
  query,
  pollIntervalSeconds,
  autoRefresh,
}: UseAlertQueueOptions): UseAlertQueueResult {
  const [items, setItems] = useState<Alert[]>([]);
  const [summary, setSummary] = useState<SummaryCounts>(EMPTY_SUMMARY);
  const [filterOptions, setFilterOptions] = useState<AlertFilterOptions>(EMPTY_FILTER_OPTIONS);
  const [filteredTotal, setFilteredTotal] = useState<number>(0);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [pageIndex, setPageIndex] = useState<number>(0);
  const [staleOnLaterPage, setStaleOnLaterPage] = useState<boolean>(false);
  const [loading, setLoading] = useState<boolean>(true);
  const [refreshing, setRefreshing] = useState<boolean>(false);
  const [lastSyncedAt, setLastSyncedAt] = useState<Date | null>(null);
  const [apiDegraded, setApiDegraded] = useState<boolean>(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  const baseIntervalSec = clampPollIntervalSeconds(pollIntervalSeconds);
  const queryRef = useRef(query);
  queryRef.current = query;

  const pageIndexRef = useRef<number>(0);
  const cursorStackRef = useRef<Array<string | null>>([null]);
  const nextCursorRef = useRef<string | null>(null);
  const summaryRef = useRef<SummaryCounts>(EMPTY_SUMMARY);
  const filteredTotalRef = useRef<number>(0);

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

  const fetchPage = useCallback(
    async (
      targetPageIndex: number,
      targetCursor: string | null,
      options: { isBackgroundPoll: boolean }
    ) => {
      if (activeAbortRef.current) {
        activeAbortRef.current.abort();
      }
      const controller = new AbortController();
      activeAbortRef.current = controller;

      if (!hasLoadedOnceRef.current && !options.isBackgroundPoll) {
        setLoading(true);
      } else {
        setRefreshing(true);
      }

      try {
        const reqQuery: ListAlertsQuery = targetCursor
          ? { ...queryRef.current, cursor: targetCursor }
          : { ...queryRef.current };
        const resp = await alertApi.listAlerts(reqQuery, controller.signal);
        if (!mountedRef.current || controller.signal.aborted) {
          return;
        }
        hasLoadedOnceRef.current = true;
        const nextItems = resp.items ?? [];
        const nextSummary = resp.summary ?? EMPTY_SUMMARY;
        const nextTotal =
          typeof resp.filtered_total === 'number' ? resp.filtered_total : nextItems.length;
        const nextCur = resp.next_cursor ?? null;

        if (options.isBackgroundPoll && targetPageIndex > 0) {
          if (
            nextSummary.firing !== summaryRef.current.firing ||
            nextSummary.acknowledged !== summaryRef.current.acknowledged ||
            nextTotal !== filteredTotalRef.current
          ) {
            setStaleOnLaterPage(true);
          }
        } else if (targetPageIndex === 0) {
          setStaleOnLaterPage(false);
        }

        summaryRef.current = nextSummary;
        filteredTotalRef.current = nextTotal;
        nextCursorRef.current = nextCur;

        setItems(nextItems);
        setSummary(nextSummary);
        if (resp.filter_options) {
          setFilterOptions({
            environments: resp.filter_options.environments ?? [],
            sites: resp.filter_options.sites ?? [],
            teams: resp.filter_options.teams ?? [],
            instances: resp.filter_options.instances ?? [],
          });
        }
        setFilteredTotal(nextTotal);
        setNextCursor(nextCur);
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
    if (!autoRefresh || (typeof document !== 'undefined' && document.visibilityState === 'hidden')) {
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
      const curIdx = pageIndexRef.current;
      const curCursor = cursorStackRef.current[curIdx] ?? null;
      await fetchPage(curIdx, curCursor, { isBackgroundPoll: true });
      if (mountedRef.current) {
        scheduleNextPoll();
      }
    }, delayMs);
  }, [autoRefresh, clearScheduledPoll, fetchPage]);

  const queryKey = JSON.stringify(query);

  useEffect(() => {
    mountedRef.current = true;
    backoffSecRef.current = baseIntervalSec;
    pageIndexRef.current = 0;
    cursorStackRef.current = [null];
    nextCursorRef.current = null;
    setPageIndex(0);
    setStaleOnLaterPage(false);

    void fetchPage(0, null, { isBackgroundPoll: false }).then(() => {
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
  }, [queryKey, baseIntervalSec, autoRefresh, fetchPage, scheduleNextPoll, clearScheduledPoll]);

  useEffect(() => {
    const handleVisibilityChange = () => {
      if (document.visibilityState === 'hidden') {
        clearScheduledPoll();
        return;
      }
      if (autoRefresh) {
        const curIdx = pageIndexRef.current;
        const curCursor = cursorStackRef.current[curIdx] ?? null;
        void fetchPage(curIdx, curCursor, { isBackgroundPoll: true }).then(() => {
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
  }, [autoRefresh, clearScheduledPoll, fetchPage, scheduleNextPoll]);

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
    const curIdx = pageIndexRef.current;
    const curCursor = cursorStackRef.current[curIdx] ?? null;
    await fetchPage(curIdx, curCursor, { isBackgroundPoll: false });
    if (mountedRef.current) {
      scheduleNextPoll();
    }
  }, [clearScheduledPoll, fetchPage, scheduleNextPoll]);

  const goNextPage = useCallback(async () => {
    const cursor = nextCursorRef.current;
    if (!cursor) {
      return;
    }
    const nextIdx = pageIndexRef.current + 1;
    const nextStack = [...cursorStackRef.current.slice(0, nextIdx), cursor];
    cursorStackRef.current = nextStack;
    pageIndexRef.current = nextIdx;
    setPageIndex(nextIdx);
    setStaleOnLaterPage(false);

    clearScheduledPoll();
    await fetchPage(nextIdx, cursor, { isBackgroundPoll: false });
    if (mountedRef.current) {
      scheduleNextPoll();
    }
  }, [clearScheduledPoll, fetchPage, scheduleNextPoll]);

  const goPrevPage = useCallback(async () => {
    if (pageIndexRef.current <= 0) {
      return;
    }
    const prevIdx = pageIndexRef.current - 1;
    const prevCursor = cursorStackRef.current[prevIdx] ?? null;
    pageIndexRef.current = prevIdx;
    setPageIndex(prevIdx);
    setStaleOnLaterPage(false);

    clearScheduledPoll();
    await fetchPage(prevIdx, prevCursor, { isBackgroundPoll: false });
    if (mountedRef.current) {
      scheduleNextPoll();
    }
  }, [clearScheduledPoll, fetchPage, scheduleNextPoll]);

  const goFirstPage = useCallback(async () => {
    cursorStackRef.current = [null];
    pageIndexRef.current = 0;
    setPageIndex(0);
    setStaleOnLaterPage(false);

    clearScheduledPoll();
    await fetchPage(0, null, { isBackgroundPoll: false });
    if (mountedRef.current) {
      scheduleNextPoll();
    }
  }, [clearScheduledPoll, fetchPage, scheduleNextPoll]);

  const upsertAlert = useCallback((updated: Alert) => {
    setItems((prev) => prev.map((item) => (item.id === updated.id ? { ...item, ...updated } : item)));
  }, []);

  return {
    items,
    summary,
    filterOptions,
    filteredTotal,
    nextCursor,
    pageIndex,
    hasNextPage: Boolean(nextCursor),
    hasPrevPage: pageIndex > 0,
    staleOnLaterPage,
    loading,
    refreshing,
    lastSyncedAt,
    apiDegraded,
    errorMessage,
    refresh,
    goNextPage,
    goPrevPage,
    goFirstPage,
    upsertAlert,
  };
}

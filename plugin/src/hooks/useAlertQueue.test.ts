import { renderHook, act, waitFor } from '@testing-library/react';
import { useAlertQueue } from './useAlertQueue';
import { alertApi, ApiClientError } from '../api/client';
import { ListAlertsResponse } from '../types/api';

jest.mock('../api/client', () => {
  const actual = jest.requireActual('../api/client');
  return {
    ...actual,
    alertApi: {
      ...actual.alertApi,
      listAlerts: jest.fn(),
    },
  };
});

const mockedListAlerts = alertApi.listAlerts as jest.MockedFunction<typeof alertApi.listAlerts>;

function makeListResponse(firingCount = 1): ListAlertsResponse {
  return {
    summary: {
      firing: firingCount,
      acknowledged: 0,
      acknowledged_without_incident: 0,
      dismissed: 0,
      resolved_open_ticket: 0,
    },
    items: [
      {
        id: '11111111-1111-4111-8111-111111111111',
        fingerprint: 'fp-1',
        starts_at: '2026-11-25T18:00:00Z',
        ends_at: null,
        state: 'firing',
        version: 1,
        alert_name: 'HighCPU',
        severity: 'critical',
        labels: { alertname: 'HighCPU', severity: 'critical' },
        annotations: { summary: 'CPU > 95%' },
        generator_url: null,
        acknowledged_by: null,
        acknowledged_by_email: null,
        acknowledged_at: null,
        dismissed_by: null,
        dismissed_at: null,
        dismiss_reason: null,
        resolved_at: null,
        previous_episode_count: 0,
        incident_drift_status: 'none',
        servicenow_incident: null,
      },
    ],
  };
}

describe('useAlertQueue', () => {
  let visibilityState: DocumentVisibilityState = 'visible';

  beforeEach(() => {
    jest.useFakeTimers();
    mockedListAlerts.mockReset();
    visibilityState = 'visible';
    Object.defineProperty(document, 'visibilityState', {
      configurable: true,
      get: () => visibilityState,
    });
  });

  afterEach(() => {
    jest.useRealTimers();
  });

  it('polls on interval, pauses when document is hidden, and resumes immediately on visible', async () => {
    mockedListAlerts.mockResolvedValue(makeListResponse(1));

    const { result } = renderHook(() =>
      useAlertQueue({
        query: { sort: 'starts_at_desc', limit: 50 },
        pollIntervalSeconds: 10,
        autoRefresh: true,
      })
    );

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(mockedListAlerts).toHaveBeenCalledTimes(1);

    // Advance one polling interval (10s)
    await act(async () => {
      jest.advanceTimersByTime(10000);
      await Promise.resolve();
    });
    expect(mockedListAlerts).toHaveBeenCalledTimes(2);

    // Hide tab — polling must pause
    visibilityState = 'hidden';
    await act(async () => {
      document.dispatchEvent(new Event('visibilitychange'));
      jest.advanceTimersByTime(60000);
      await Promise.resolve();
    });
    expect(mockedListAlerts).toHaveBeenCalledTimes(2);

    // Return to visible — immediate refresh triggered
    visibilityState = 'visible';
    await act(async () => {
      document.dispatchEvent(new Event('visibilitychange'));
      await Promise.resolve();
    });
    expect(mockedListAlerts).toHaveBeenCalledTimes(3);
  });

  it('aborts in-flight stale request when manual refresh is triggered', async () => {
    const signals: AbortSignal[] = [];
    mockedListAlerts.mockImplementation((_q, signal) => {
      if (signal) {
        signals.push(signal);
      }
      return new Promise((resolve) => {
        if (signals.length >= 2) {
          resolve(makeListResponse(2));
        }
      });
    });

    const { result } = renderHook(() =>
      useAlertQueue({
        query: {},
        pollIntervalSeconds: 15,
        autoRefresh: false,
      })
    );

    expect(signals.length).toBe(1);
    expect(signals[0].aborted).toBe(false);

    await act(async () => {
      await result.current.refresh();
    });

    expect(signals.length).toBe(2);
    expect(signals[0].aborted).toBe(true);
    expect(signals[1].aborted).toBe(false);
    expect(result.current.summary.firing).toBe(2);
  });

  it('retains last known alerts and enters degraded mode on 503 with exponential backoff', async () => {
    mockedListAlerts
      .mockResolvedValueOnce(makeListResponse(3))
      .mockRejectedValueOnce(new ApiClientError(503, 'API_UNAVAILABLE', 'API unreachable'))
      .mockResolvedValueOnce(makeListResponse(4));

    const { result } = renderHook(() =>
      useAlertQueue({
        query: {},
        pollIntervalSeconds: 10,
        autoRefresh: true,
      })
    );

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.items).toHaveLength(1);
    expect(result.current.apiDegraded).toBe(false);

    // First scheduled poll at 10s fails with 503
    await act(async () => {
      jest.advanceTimersByTime(10000);
      await Promise.resolve();
    });

    expect(result.current.apiDegraded).toBe(true);
    expect(result.current.items).toHaveLength(1);
    expect(result.current.summary.firing).toBe(3);

    // Next backoff interval is 20s (not 10s)
    await act(async () => {
      jest.advanceTimersByTime(10000);
      await Promise.resolve();
    });
    expect(mockedListAlerts).toHaveBeenCalledTimes(2);

    await act(async () => {
      jest.advanceTimersByTime(10000);
      await Promise.resolve();
    });
    expect(mockedListAlerts).toHaveBeenCalledTimes(3);
    expect(result.current.apiDegraded).toBe(false);
    expect(result.current.summary.firing).toBe(4);
  });
});

import type { ReactNode } from 'react';
import { renderHook, waitFor, act } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { sameSpeedMap, useClients } from '@/hooks/useClients';
import { makeTestQueryClient } from '@/test/test-utils';
import { HttpUtil, Msg } from '@/utils';
import type { ClientPageResponse, ClientsSummary } from '@/schemas/client';
import { acknowledgedAccounting, pendingAccounting } from './fixtures/client-policy-accounting';

afterEach(() => {
  vi.restoreAllMocks();
});

describe('websocket payload identity preservation', () => {
  const speed = (up: number, down: number) => ({ up, down });

  it('treats an unchanged speed map as unchanged', () => {
    const a = { 'a@x': speed(1, 2), 'b@x': speed(3, 4) };
    expect(sameSpeedMap(a, { 'a@x': speed(1, 2), 'b@x': speed(3, 4) })).toBe(true);
    expect(sameSpeedMap(a, { 'a@x': speed(1, 2) })).toBe(false);
    expect(sameSpeedMap(a, { 'a@x': speed(1, 2), 'b@x': speed(3, 5) })).toBe(false);
    expect(sameSpeedMap(a, { 'a@x': speed(1, 2), 'c@x': speed(3, 4) })).toBe(false);
    expect(sameSpeedMap({}, {})).toBe(true);
  });
});

describe('client summary always reflects the server, never a client_stats recompute (#6116)', () => {
  const serverSummary: ClientsSummary = {
    total: 3,
    active: 3,
    onlineCount: 0,
    depletedCount: 0,
    expiringCount: 0,
    deactiveCount: 0,
    online: [],
    depleted: [],
    expiring: [],
    deactive: [],
  };

  const pagedResponse = {
    items: [],
    total: 3,
    filtered: 3,
    page: 1,
    pageSize: 25,
    groups: [],
    summary: serverSummary,
  };

  function mockPanel(response: ClientPageResponse = pagedResponse) {
    vi.spyOn(HttpUtil, 'get').mockImplementation(async (url: string) => {
      if (url.includes('/clients/list/paged')) return new Msg(true, '', response);
      if (url.includes('/inbounds/options')) return new Msg(true, '', []);
      return new Msg(true, '', null);
    });
    vi.spyOn(HttpUtil, 'post').mockImplementation(async (url: string) => {
      if (url.includes('/setting/defaultSettings')) return new Msg(true, '', { pageSize: 25 });
      if (url.includes('/clients/onlines')) return new Msg(true, '', []);
      return new Msg(true, '', null);
    });
  }

  function wrapperFor() {
    const queryClient = makeTestQueryClient();
    return ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );
  }

  async function loadedHook(response: ClientPageResponse = pagedResponse) {
    mockPanel(response);
    const { result } = renderHook(() => useClients(), { wrapper: wrapperFor() });
    await waitFor(() => expect(result.current.settingsReady).toBe(true));
    act(() => {
      result.current.setQuery({ page: 1, pageSize: 25, sort: 'createdAt', order: 'ascend' });
    });
    await waitFor(() => expect(result.current.fetched).toBe(true));
    expect(result.current.summary).toEqual(response.summary);
    return result;
  }

  it('updates accounting-only snapshots and clears an old identity when its next snapshot has no ledger', async () => {
    const result = await loadedHook({
      ...pagedResponse,
      total: 1,
      filtered: 1,
      summary: { ...serverSummary, total: 1, active: 1 },
      items: [
        {
          email: 'a@x',
          enable: true,
          totalGB: 100,
          traffic: { up: 50, down: 50, accounting: pendingAccounting },
        },
      ],
    });
    act(() =>
      result.current.applyClientStatsEvent({
        clients: [{ email: 'a@x', up: 50, down: 50, accounting: acknowledgedAccounting }],
      }),
    );
    await waitFor(() =>
      expect(result.current.clients[0].traffic?.accounting).toEqual(acknowledgedAccounting),
    );
    act(() =>
      result.current.applyClientStatsEvent({
        clients: [{ email: 'a@x', up: 0, down: 0, total: 100 }],
      }),
    );
    await waitFor(() => expect(result.current.clients[0].traffic?.accounting).toBeUndefined());
  });

  it('stays pinned to the server summary across a client_stats push carrying an orphan row with no matching gap', async () => {
    const result = await loadedHook();

    act(() => {
      result.current.applyClientStatsEvent({
        snapshot: true,
        clients: [
          { email: 'a@x', enable: true, up: 0, down: 0, total: 0, expiryTime: 0 },
          { email: 'b@x', enable: true, up: 0, down: 0, total: 0, expiryTime: 0 },
          { email: 'c@x', enable: true, up: 0, down: 0, total: 0, expiryTime: 0 },
          { email: 'ghost@x', enable: false, up: 0, down: 0, total: 1, expiryTime: 1 },
        ],
      });
    });

    expect(result.current.summary).toEqual(serverSummary);
  });

  it('stays pinned to the server summary across a client_stats push where an orphan and a gap net out to the server total', async () => {
    const result = await loadedHook();

    act(() => {
      result.current.applyClientStatsEvent({
        snapshot: true,
        clients: [
          { email: 'a@x', enable: true, up: 0, down: 0, total: 0, expiryTime: 0 },
          { email: 'b@x', enable: true, up: 0, down: 0, total: 0, expiryTime: 0 },
          { email: 'ghost@x', enable: false, up: 0, down: 0, total: 1, expiryTime: 1 },
        ],
      });
    });

    expect(result.current.summary).toEqual(serverSummary);
  });
});

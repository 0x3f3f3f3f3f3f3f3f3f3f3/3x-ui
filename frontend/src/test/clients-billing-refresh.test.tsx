import type { ReactNode } from 'react';
import { act, cleanup, renderHook } from '@testing-library/react';
import { focusManager, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, expect, it, vi } from 'vitest';
import { useClients } from '@/hooks/useClients';
import { makeTestQueryClient } from './test-utils';
import { HttpUtil, Msg } from '@/utils';

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  focusManager.setFocused(undefined);
  vi.restoreAllMocks();
});

it('refreshes billed balances despite frequent raw-stat pushes and pauses while hidden', async () => {
  vi.useFakeTimers();
  focusManager.setFocused(true);
  let billed = '0';
  let requests = 0;
  vi.spyOn(HttpUtil, 'get').mockImplementation(async (url) => {
    if (url.includes('/clients/list/paged')) {
      requests++;
      return new Msg(true, '', {
        items: [
          {
            email: 'ssh-user',
            traffic: { up: 0, down: 0, total: 104857600, enable: true },
            billing: {
              up: billed === '0' ? '0' : '16384',
              down: billed === '0' ? '0' : '16384',
              billed,
              remainder: 0,
              quota: '104857600',
              remaining: billed === '0' ? '104857600' : '104808448',
              unlimited: false,
              multiplier: '1.5',
              exhausted: false,
            },
          },
        ],
        total: 1,
        filtered: 1,
        page: 1,
        pageSize: 25,
        groups: [],
        summary: {
          total: 1,
          active: 1,
          online: [],
          depleted: [],
          expiring: [],
          deactive: [],
          onlineCount: 0,
          depletedCount: 0,
          expiringCount: 0,
          deactiveCount: 0,
        },
      });
    }
    return new Msg(true, '', []);
  });
  vi.spyOn(HttpUtil, 'post').mockImplementation(
    async (url) => new Msg(true, '', url.includes('/clients/onlines') ? [] : { pageSize: 25 }),
  );
  const queryClient = makeTestQueryClient();
  const { result, unmount } = renderHook(() => useClients(), {
    wrapper: ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    ),
  });
  act(() => result.current.setQuery({ page: 1, pageSize: 25 }));
  await act(async () => {
    await vi.advanceTimersByTimeAsync(10);
  });
  expect(result.current.clients[0]?.billing?.billed).toBe('0');
  billed = '49152';

  for (let tick = 1; tick <= 7; tick++) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000);
    });
    act(() => result.current.applyClientStatsEvent({ clients: [{ email: 'ssh-user', up: tick }] }));
  }
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(result.current.clients[0]?.billing?.billed).toBe('49152');
  expect(result.current.clients[0]?.traffic?.up).toBe(7);

  focusManager.setFocused(false);
  const visibleRequests = requests;
  await act(async () => {
    await vi.advanceTimersByTimeAsync(10000);
  });
  expect(requests).toBe(visibleRequests);
  unmount();
  focusManager.setFocused(true);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(10000);
  });
  expect(requests).toBe(visibleRequests);
  queryClient.clear();
});

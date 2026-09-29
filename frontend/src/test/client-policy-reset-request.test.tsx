import type { ReactNode } from 'react';
import { act, renderHook } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';
import { afterEach, expect, it, vi } from 'vitest';

import { useClients } from '@/hooks/useClients';
import { setupHttp } from '@/api/http-init';
import { pendingAccounting } from '@/test/fixtures/client-policy-accounting';
import { makeTestQueryClient } from '@/test/test-utils';
import { HttpUtil, Msg } from '@/utils';

afterEach(() => {
  vi.restoreAllMocks();
  sessionStorage.clear();
  delete window.X_UI_BASE_PATH;
  setupHttp();
});

it('keeps a reset-all request across uncertain replies and remount so the server can reuse the original membership', async () => {
  window.X_UI_BASE_PATH = '/panel-a/';
  setupHttp();
  vi.spyOn(HttpUtil, 'get').mockResolvedValue(new Msg(true, '', null));
  const bodies: unknown[] = [];
  vi.spyOn(HttpUtil, 'post').mockImplementation(async (url, body) => {
    if (!url.endsWith('/resetAllTraffics')) return new Msg(true, '', {});
    bodies.push(body);
    return new Msg(bodies.length > 1, '', null);
  });
  const queryClient = makeTestQueryClient();
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
  const first = renderHook(() => useClients({ list: false }), { wrapper });
  await act(async () => {
    await first.result.current.resetAllTraffics();
  });
  expect(bodies[0]).toEqual({ requestId: expect.stringMatching(/^[a-f0-9-]{36}$/) });
  first.unmount();
  window.X_UI_BASE_PATH = '/panel-b/';
  setupHttp();
  const second = renderHook(() => useClients({ list: false }), { wrapper });
  await act(async () => {
    await second.result.current.resetAllTraffics();
  });
  expect(bodies[1]).not.toEqual(bodies[0]);
  window.X_UI_BASE_PATH = '/panel-a/';
  setupHttp();
  await act(async () => {
    await second.result.current.resetAllTraffics();
  });
  expect(bodies[2]).toEqual(bodies[0]);
  await act(async () => {
    await second.result.current.resetAllTraffics();
  });
  expect(bodies[3]).toEqual({ requestId: expect.stringMatching(/^[a-f0-9-]{36}$/) });
  expect(bodies[3]).not.toEqual(bodies[0]);
});

it('reuses a managed reset after an uncertain response and remount, including rename, then starts a new request after success', async () => {
  vi.spyOn(HttpUtil, 'get').mockResolvedValue(new Msg(true, '', null));
  const bodies: unknown[] = [];
  const urls: string[] = [];
  vi.spyOn(HttpUtil, 'post').mockImplementation(async (url, body) => {
    if (!url.includes('/resetTraffic/')) return new Msg(true, '', {});
    bodies.push(body);
    urls.push(url);
    return new Msg(bodies.length !== 1, '', null);
  });
  const queryClient = makeTestQueryClient();
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
  const client = { email: 'old@example.com', traffic: { accounting: pendingAccounting } };
  const first = renderHook(() => useClients({ list: false }), { wrapper });
  await act(async () => {
    await first.result.current.resetTraffic(client);
  });
  expect(bodies[0]).toEqual({
    clientId: pendingAccounting.clientId,
    requestId: expect.stringMatching(/^[a-f0-9-]{36}$/),
  });
  first.unmount();

  const second = renderHook(() => useClients({ list: false }), { wrapper });
  const renamed = { ...client, email: 'new@example.com' };
  await act(async () => {
    await second.result.current.resetTraffic(renamed);
  });
  expect(bodies[1]).toEqual(bodies[0]);
  expect(urls).toEqual([
    '/panel/api/clients/resetTraffic/old%40example.com',
    '/panel/api/clients/resetTraffic/new%40example.com',
  ]);
  await act(async () => {
    await second.result.current.resetTraffic(renamed);
  });
  expect(bodies[2]).toEqual({
    clientId: pendingAccounting.clientId,
    requestId: expect.stringMatching(/^[a-f0-9-]{36}$/),
  });
  expect(bodies[2]).not.toEqual(bodies[0]);
});

it('retains the request after a network error and isolates a new identity reusing the email', async () => {
  vi.spyOn(HttpUtil, 'get').mockResolvedValue(new Msg(true, '', null));
  const bodies: unknown[] = [];
  vi.spyOn(HttpUtil, 'post').mockImplementation(async (url, body) => {
    if (!url.includes('/resetTraffic/')) return new Msg(true, '', {});
    bodies.push(body);
    if (bodies.length < 3) throw new Error('response lost');
    return new Msg(true, '', null);
  });
  const queryClient = makeTestQueryClient();
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
  const { result } = renderHook(() => useClients({ list: false }), { wrapper });
  const client = { email: 'reused@example.com', traffic: { accounting: pendingAccounting } };
  for (let attempt = 0; attempt < 2; attempt++) {
    await act(async () => {
      await expect(result.current.resetTraffic(client)).rejects.toThrow('response lost');
    });
  }
  expect(bodies[0]).toEqual({
    clientId: pendingAccounting.clientId,
    requestId: expect.stringMatching(/^[a-f0-9-]{36}$/),
  });
  expect(bodies[1]).toEqual(bodies[0]);
  const newId = 'b1f04f37-d87f-46e7-9221-152975f17999';
  await act(async () => {
    await result.current.resetTraffic({
      ...client,
      traffic: { accounting: { ...pendingAccounting, clientId: newId } },
    });
  });
  expect(bodies[2]).toEqual({
    clientId: newId,
    requestId: expect.stringMatching(/^[a-f0-9-]{36}$/),
  });
  expect(bodies[2]).not.toEqual(bodies[0]);
});

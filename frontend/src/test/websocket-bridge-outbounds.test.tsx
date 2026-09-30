import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { act, renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ReactNode } from 'react';

import { getSharedWebSocketClient } from '@/api/websocket';
import { useWebSocketBridge } from '@/api/websocketBridge';
import { keys } from '@/api/queryKeys';
import { useOutboundTagGroups } from '@/api/queries/useOutboundTags';
import { markLocalInvalidate } from '@/api/invalidationTracker';
import { HttpUtil } from '@/utils';

type ListenerMap = { listeners: Map<string, Set<(payload: unknown) => void>> };

describe('websocket bridge outbounds handler', () => {
  let originalWS: typeof WebSocket;
  let deliverMessage: (message: unknown) => void;

  beforeEach(() => {
    originalWS = globalThis.WebSocket;
    getSharedWebSocketClient().disconnect();
    class FakeWebSocket extends EventTarget {
      static readonly CONNECTING = 0;
      static readonly OPEN = 1;
      static readonly CLOSING = 2;
      static readonly CLOSED = 3;
      readyState = FakeWebSocket.CONNECTING;
      constructor() {
        super();
        deliverMessage = (message) =>
          this.dispatchEvent(
            new MessageEvent('message', {
              data: JSON.stringify(message),
            }),
          );
      }
      close() {}
      send() {}
    }
    globalThis.WebSocket = FakeWebSocket as unknown as typeof WebSocket;
  });

  afterEach(() => {
    getSharedWebSocketClient().disconnect();
    globalThis.WebSocket = originalWS;
  });

  it('refreshes cached outbound choices after a background subscription invalidation', async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let subscriptionTag = 'old-proxy';
    vi.mocked(HttpUtil.post).mockImplementation(async () => ({
      success: true,
      msg: '',
      obj: JSON.stringify({
        xraySetting: { outbounds: [{ tag: 'direct', protocol: 'freedom' }] },
        subscriptionOutboundTags: [subscriptionTag],
      }),
    }));
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );
    const { result, unmount } = renderHook(
      () => {
        useWebSocketBridge();
        return useOutboundTagGroups();
      },
      { wrapper },
    );
    try {
      await waitFor(() => expect(result.current.data?.outbounds).toEqual(['direct', 'old-proxy']));
      subscriptionTag = 'new-proxy';
      // An unrelated local mutation and a neighboring invalidation must not
      // suppress an outbound subscription update.
      markLocalInvalidate();
      act(() => {
        for (const type of ['outbounds', 'inbounds']) {
          deliverMessage({ type: 'invalidate', payload: { type } });
        }
      });
      await waitFor(() => expect(result.current.data?.outbounds).toEqual(['direct', 'new-proxy']));
    } finally {
      unmount();
      queryClient.clear();
    }
  });

  it('ignores a non-array outbounds push instead of poisoning the cache', () => {
    const queryClient = new QueryClient();
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );

    renderHook(() => useWebSocketBridge(), { wrapper });

    const client = getSharedWebSocketClient() as unknown as ListenerMap;
    const handlers = client.listeners.get('outbounds');
    expect(handlers && handlers.size).toBeGreaterThan(0);

    for (const handler of handlers ?? []) handler({ not: 'an array' });

    expect(queryClient.getQueryData(keys.xray.outboundsTraffic())).toBeUndefined();
  });
});

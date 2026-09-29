import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { onlineManager } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { keys } from '@/api/queryKeys';
import { useInbounds } from '@/pages/inbounds/useInbounds';
import InboundList from '@/pages/inbounds/list/InboundList';
import { HttpUtil, Msg } from '@/utils';
import { makeTestQueryClient, renderWithProviders } from './test-utils';

afterEach(() => {
  vi.useRealTimers();
  onlineManager.setOnline(true);
  vi.restoreAllMocks();
});

function row(protocol = 'mieru') {
  return {
    id: 1,
    protocol,
    tag: 'test-inbound',
    remark: 'Observed service',
    enable: true,
    port: 2222,
    settings: { clients: [{ email: 'client', enable: true }] },
  };
}

function fixture(protocol = 'mieru', isMobile = false) {
  const queryClient = makeTestQueryClient();
  queryClient.setQueryData(keys.inbounds.slim(), [row(protocol)]);
  queryClient.setQueryData(keys.clients.onlines(), []);
  queryClient.setQueryData(keys.clients.onlinesByGuid(), {});
  queryClient.setQueryData(keys.clients.activeInbounds(), {});
  queryClient.setQueryData(keys.clients.lastOnline(), {});
  queryClient.setQueryData(keys.settings.defaults(), {});
  function List() {
    const data = useInbounds();
    return (
      <MemoryRouter>
        <InboundList
          {...data}
          isMobile={isMobile}
          subEnable={false}
          nodesById={new Map()}
          hasActiveNode={false}
          hosts={[]}
          onAddInbound={() => {}}
          onGeneralAction={() => {}}
          onRowAction={() => {}}
          onBulkDelete={async () => false}
        />
      </MemoryRouter>
    );
  }
  const rendered = renderWithProviders(<List />, { queryClient });
  return { queryClient, ...rendered };
}

describe('mieru runtime state in the inbound list', () => {
  it.each([false, true])(
    'keeps an enabled but protected listener visible (mobile=%s)',
    async (mobile) => {
      vi.spyOn(HttpUtil, 'get').mockResolvedValue(
        new Msg(true, '', [
          {
            inboundId: 1,
            state: 'protected',
            reason: 'listener unavailable',
            authenticatedSessions: 0,
          },
        ]),
      );
      fixture('mieru', mobile);
      const badge = await screen.findByRole('status', { name: 'mieru runtime: Protected' });
      expect(screen.getByRole('switch').getAttribute('aria-checked')).toBe('true');
      expect(badge.textContent).toContain('Protected');
      fireEvent.mouseOver(badge);
      expect(
        await screen.findByText(
          'mieru listener unavailable. Check the listening address and port.',
        ),
      ).toBeTruthy();
      expect(screen.queryByText('Running')).toBeNull();
    },
  );

  it('polls real query state and clears a cached running result after a failed refresh', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
    const get = vi
      .spyOn(HttpUtil, 'get')
      .mockResolvedValueOnce(
        new Msg(true, '', [
          { inboundId: 1, state: 'running', reason: '', authenticatedSessions: 2 },
        ]),
      )
      .mockResolvedValue(new Msg(false, 'database unavailable', null));
    const { queryClient } = fixture();
    const running = await screen.findByRole('status', { name: 'mieru runtime: Running' });
    expect(running.textContent).toContain('2');
    fireEvent.mouseOver(running);
    expect(await screen.findByText('Authenticated mieru sessions: 2')).toBeTruthy();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3000);
    });
    await waitFor(
      () => expect(screen.getByRole('status', { name: 'mieru runtime: Unavailable' })).toBeTruthy(),
      { timeout: 5000 },
    );
    expect(
      get.mock.calls.filter(([url]) => url === '/panel/api/inbounds/mieru/status'),
    ).toHaveLength(2);
    expect(screen.queryByRole('status', { name: 'mieru runtime: Running' })).toBeNull();
    get.mockResolvedValue(
      new Msg(true, '', [
        { inboundId: 1, state: 'pending', reason: 'awaiting disable', authenticatedSessions: 2 },
      ]),
    );
    await act(async () => {
      await queryClient.invalidateQueries({ queryKey: keys.inbounds.mieruStatus() });
    });
    expect(await screen.findByRole('status', { name: 'mieru runtime: Pending' })).toHaveProperty(
      'textContent',
      'Pending · 2',
    );
  });

  it.each([
    ['missing row', []],
    [
      'unknown state',
      [
        {
          inboundId: 1,
          state: 'future-state',
          reason: 'private diagnostic',
          authenticatedSessions: 5,
        },
      ],
    ],
    ['invalid count', [{ inboundId: 1, state: 'running', reason: '', authenticatedSessions: '2' }]],
    ['negative count', [{ inboundId: 1, state: 'running', reason: '', authenticatedSessions: -1 }]],
    ['null payload', null],
  ])('shows unavailable for %s instead of trusting configured enable', async (_label, payload) => {
    vi.spyOn(HttpUtil, 'get').mockResolvedValue(new Msg(true, '', payload));
    const { queryClient } = fixture();
    await waitFor(() => expect(HttpUtil.get).toHaveBeenCalled());
    await waitFor(() =>
      expect(queryClient.getQueryState(keys.inbounds.mieruStatus())?.fetchStatus).toBe('idle'),
    );
    expect(await screen.findByRole('status', { name: 'mieru runtime: Unavailable' })).toBeTruthy();
    expect(screen.queryByRole('status', { name: 'mieru runtime: Running' })).toBeNull();
    expect(screen.queryByText('private diagnostic')).toBeNull();
  });

  it('hides cached running status while offline and recovers after a fresh response', async () => {
    const get = vi
      .spyOn(HttpUtil, 'get')
      .mockResolvedValue(
        new Msg(true, '', [
          { inboundId: 1, state: 'running', reason: '', authenticatedSessions: 1 },
        ]),
      );
    const { queryClient } = fixture();
    await screen.findByRole('status', { name: 'mieru runtime: Running' });
    act(() => {
      onlineManager.setOnline(false);
      void queryClient.invalidateQueries({ queryKey: keys.inbounds.mieruStatus() });
    });
    await screen.findByRole('status', { name: 'mieru runtime: Unavailable' });
    expect(screen.queryByRole('status', { name: 'mieru runtime: Running' })).toBeNull();
    get.mockResolvedValue(
      new Msg(true, '', [{ inboundId: 1, state: 'running', reason: '', authenticatedSessions: 3 }]),
    );
    act(() => onlineManager.setOnline(true));
    expect(await screen.findByRole('status', { name: 'mieru runtime: Running' })).toHaveProperty(
      'textContent',
      'Running · 3',
    );
  });

  it('only polls when the list contains mieru inbounds', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
    const get = vi.spyOn(HttpUtil, 'get').mockResolvedValue(
      new Msg(true, '', [
        {
          inboundId: 1,
          state: 'idle',
          reason: 'no enabled clients',
          authenticatedSessions: 0,
        },
      ]),
    );
    const { queryClient } = fixture('vless');
    await screen.findByText('Observed service');
    expect(get).not.toHaveBeenCalled();
    expect(screen.queryByRole('status')).toBeNull();
    act(() => queryClient.setQueryData(keys.inbounds.slim(), [row()]));
    expect(await screen.findByRole('status', { name: 'mieru runtime: Idle' })).toBeTruthy();
    act(() => queryClient.setQueryData(keys.inbounds.slim(), [row('vless')]));
    await waitFor(() => expect(screen.queryByRole('status')).toBeNull());
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3300);
    });
    expect(
      get.mock.calls.filter(([url]) => url === '/panel/api/inbounds/mieru/status'),
    ).toHaveLength(1);
  });
});

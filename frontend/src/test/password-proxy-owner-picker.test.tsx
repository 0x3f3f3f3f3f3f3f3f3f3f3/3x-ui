import { describe, expect, it, vi } from 'vitest';
import { act, fireEvent, renderHook, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';

import InboundFormModal from '@/pages/inbounds/form/InboundFormModal';
import { DBInbound } from '@/models/dbinbound';
import { HttpUtil } from '@/utils';
import { useClients } from '@/hooks/useClients';
import { queryClient as appQueryClient } from '@/queryClient';
import { chooseSelectOption, renderWithProviders } from './test-utils';

const firstID = 'a6426bfc-42c6-45d6-8182-1108f7986d89';
const secondID = '97af23ee-90d3-48d3-bb9f-790322466bea';

function renderPassword(
  protocol: 'mixed' | 'http',
  {
    remote = false,
    auth = 'password',
    owned = true,
    queryClient,
  }: { remote?: boolean; auth?: string; owned?: boolean; queryClient?: QueryClient } = {},
) {
  const accounts = [
    { user: 'resource-user', pass: 'resource-password', ...(owned && { ownerClientId: firstID }) },
    { user: 'resource-alias', pass: 'alias-password', ...(owned && { ownerClientId: firstID }) },
  ];
  const inbound = new DBInbound({
    id: 42,
    protocol,
    port: 24101,
    nodeId: remote ? 7 : null,
    settings: protocol === 'mixed' ? { auth, accounts, udp: true } : { accounts },
  });
  return renderWithProviders(
    <InboundFormModal
      open
      mode="edit"
      dbInbound={inbound}
      dbInbounds={[inbound]}
      availableNodes={[]}
      onClose={() => {}}
      onSaved={() => {}}
    />,
    { queryClient },
  );
}

function mockOwners() {
  const get = vi.mocked(HttpUtil.get);
  get.mockImplementation(async (url) => ({
    success: true,
    msg: '',
    obj: String(url).includes('/clients/list/paged')
      ? {
          items: [
            { clientId: firstID, email: 'first-owner', password: 'global-secret' },
            { clientId: secondID, email: 'selected-owner', password: 'second-global-secret' },
          ],
          total: 2,
          filtered: 2,
          page: 1,
          pageSize: 25,
        }
      : [],
  }));
  return get;
}

function choosePasswordOwner(index: number, label: string) {
  const input = document.getElementById(`passwordOwner-${index}`);
  if (!input) throw new Error('missing owner picker');
  fireEvent.mouseDown(input.closest('.ant-select')!);
  const listId = input.getAttribute('aria-controls');
  const dropdown = listId ? document.getElementById(listId)?.closest('.ant-select-dropdown') : null;
  const option = Array.from(dropdown?.querySelectorAll('.ant-select-item-option') ?? []).find(
    (item) => (item.getAttribute('title') ?? item.textContent ?? '').trim() === label,
  );
  if (!option) throw new Error(`missing owner option for row ${index}: ${label}`);
  fireEvent.click(option);
}

function save() {
  const button = document.querySelector('.ant-modal-footer .ant-btn-primary');
  if (!button) throw new Error('missing Save button');
  fireEvent.click(button);
}

describe('password proxy owner picker', () => {
  it.each(['mixed', 'http'] as const)(
    'selects an existing %s owner without replacing either resource credential',
    async (protocol) => {
      mockOwners();
      const post = vi.mocked(HttpUtil.post);
      post.mockClear();
      renderPassword(protocol);
      const pickers = await screen.findAllByLabelText('Owner client');
      expect(pickers).toHaveLength(2);
      fireEvent.mouseDown(pickers[0].closest('.ant-select')!);
      await screen.findByText('selected-owner', { selector: '.ant-select-item-option-content' });
      choosePasswordOwner(0, 'selected-owner');
      save();
      await waitFor(() =>
        expect(post.mock.calls.some(([url]) => String(url).endsWith('/update/42'))).toBe(true),
      );
      const payload = post.mock.calls.find(([url]) =>
        String(url).endsWith('/update/42'),
      )![1] as Record<string, unknown>;
      const settings = JSON.parse(payload.settings as string);
      expect(settings.accounts).toEqual([
        { user: 'resource-user', pass: 'resource-password', ownerClientId: secondID },
        { user: 'resource-alias', pass: 'alias-password', ownerClientId: firstID },
      ]);
      expect(settings.clients).toBeUndefined();
      expect(payload.clientStats).toBeUndefined();
      expect(JSON.stringify(payload)).not.toContain('global-secret');
    },
  );

  it('requires every row owner before saving a newly owned list', async () => {
    mockOwners();
    const post = vi.mocked(HttpUtil.post);
    post.mockClear();
    renderPassword('http', { owned: false });
    const pickers = await screen.findAllByLabelText('Owner client');
    fireEvent.mouseDown(pickers[0].closest('.ant-select')!);
    await screen.findByText('selected-owner', { selector: '.ant-select-item-option-content' });
    choosePasswordOwner(0, 'selected-owner');
    save();
    await screen.findByText('Select an owner client');
    expect(post.mock.calls.some(([url]) => String(url).endsWith('/update/42'))).toBe(false);
    expect(screen.getByRole('tab', { name: 'Protocol' }).getAttribute('aria-selected')).toBe(
      'true',
    );
    choosePasswordOwner(1, 'selected-owner');
    save();
    await waitFor(() =>
      expect(post.mock.calls.some(([url]) => String(url).endsWith('/update/42'))).toBe(true),
    );
    const payload = post.mock.calls.find(([url]) => String(url).endsWith('/update/42'))![1] as {
      settings: string;
    };
    expect(
      JSON.parse(payload.settings).accounts.map(
        (row: { ownerClientId: string }) => row.ownerClientId,
      ),
    ).toEqual([secondID, secondID]);
  });

  it.each([
    { protocol: 'mixed' as const, remote: false, auth: 'noauth' },
    { protocol: 'mixed' as const, remote: true, auth: 'password' },
    { protocol: 'http' as const, remote: true, auth: 'password' },
  ])(
    'keeps $protocol remote=$remote auth=$auth ownership read-only and legacy credentials editable',
    async ({ protocol, remote, auth }) => {
      const get = mockOwners();
      const post = vi.mocked(HttpUtil.post);
      post.mockClear();
      renderPassword(protocol, { remote, auth, owned: false });
      const pickers = await screen.findAllByLabelText('Owner client');
      expect(pickers.every((picker) => (picker as HTMLInputElement).disabled)).toBe(true);
      save();
      await waitFor(() =>
        expect(post.mock.calls.some(([url]) => String(url).endsWith('/update/42'))).toBe(true),
      );
      expect(get.mock.calls.some(([url]) => String(url).includes('/clients/list/paged'))).toBe(
        false,
      );
      const payload = post.mock.calls.find(([url]) => String(url).endsWith('/update/42'))![1] as {
        settings: string;
      };
      expect(JSON.parse(payload.settings).accounts).toEqual([
        { user: 'resource-user', pass: 'resource-password' },
        { user: 'resource-alias', pass: 'alias-password' },
      ]);
    },
  );

  it('rejects switching owned Mixed accounts to noauth without deleting ownership', async () => {
    mockOwners();
    const post = vi.mocked(HttpUtil.post);
    post.mockClear();
    renderPassword('mixed');
    await screen.findAllByLabelText('Owner client');
    fireEvent.click(screen.getByRole('tab', { name: 'Protocol' }));
    const auth = screen.getByLabelText('Auth');
    await waitFor(() =>
      expect(
        document.getElementById('passwordOwner-0')!.closest('.ant-select')!.textContent,
      ).toContain('first-owner'),
    );
    chooseSelectOption(auth.id, 'noauth');
    save();
    await screen.findAllByText('Owned accounts require password authentication.');
    expect(post.mock.calls.some(([url]) => String(url).endsWith('/update/42'))).toBe(false);
    expect(
      document.getElementById('passwordOwner-0')!.closest('.ant-select')!.textContent,
    ).toContain('first-owner');
    chooseSelectOption(auth.id, 'password');
    save();
    await waitFor(() =>
      expect(post.mock.calls.some(([url]) => String(url).endsWith('/update/42'))).toBe(true),
    );
    const payload = post.mock.calls.find(([url]) => String(url).endsWith('/update/42'))![1] as {
      settings: string;
    };
    expect(JSON.parse(payload.settings).accounts[0]).toEqual({
      user: 'resource-user',
      pass: 'resource-password',
      ownerClientId: firstID,
    });
  });

  it('loads further pages and preserves a selected label outside later searches', async () => {
    const get = vi.mocked(HttpUtil.get);
    get.mockImplementation(async (url) => {
      if (!String(url).includes('/clients/list/paged')) return { success: true, msg: '', obj: [] };
      const params = new URL(String(url), 'http://panel.test').searchParams;
      const page = Number(params.get('page'));
      const searching = params.get('search') === 'needle';
      return {
        success: true,
        msg: '',
        obj: {
          items: searching
            ? []
            : page === 1
              ? [{ clientId: firstID, email: 'first-owner' }]
              : [{ clientId: secondID, email: 'selected-owner' }],
          total: 26,
          filtered: searching ? 0 : 26,
          page,
          pageSize: 25,
        },
      };
    });
    renderPassword('http');
    const picker = (await screen.findAllByLabelText('Owner client'))[0];
    fireEvent.click(screen.getByRole('tab', { name: 'Protocol' }));
    fireEvent.mouseDown(picker.closest('.ant-select')!);
    fireEvent.click(await screen.findByRole('button', { name: 'Load more clients' }));
    await screen.findByText('selected-owner', { selector: '.ant-select-item-option-content' });
    choosePasswordOwner(0, 'selected-owner');
    fireEvent.change(picker, { target: { value: 'needle' } });
    await waitFor(() =>
      expect(get.mock.calls.some(([url]) => String(url).includes('search=needle'))).toBe(true),
    );
    expect(picker.closest('.ant-select')!.textContent).toContain('selected-owner');
    const calls = get.mock.calls.filter(([url]) => String(url).includes('/clients/list/paged'));
    expect(
      calls.every(
        ([url]) => new URL(String(url), 'http://panel.test').searchParams.get('pageSize') === '25',
      ),
    ).toBe(true);
    expect(
      calls.some(
        ([url]) => new URL(String(url), 'http://panel.test').searchParams.get('page') === '2',
      ),
    ).toBe(true);
  });

  it('keeps a persisted owner label when search results omit it without selecting again', async () => {
    const post = vi.mocked(HttpUtil.post);
    post.mockClear();
    vi.mocked(HttpUtil.get).mockImplementation(async (url) => {
      if (!String(url).includes('/clients/list/paged')) return { success: true, msg: '', obj: [] };
      const searching = String(url).includes('search=needle');
      return {
        success: true,
        msg: '',
        obj: {
          items: searching
            ? []
            : [
                { clientId: firstID, email: 'first-owner' },
                { clientId: secondID, email: 'second-owner' },
              ],
          total: 2,
          filtered: searching ? 0 : 2,
          page: 1,
          pageSize: 25,
        },
      };
    });
    renderPassword('http');
    const picker = (await screen.findAllByLabelText('Owner client'))[0];
    await waitFor(() =>
      expect(picker.closest('.ant-select')!.textContent).toContain('first-owner'),
    );
    fireEvent.click(screen.getByRole('tab', { name: 'Protocol' }));
    fireEvent.mouseDown(picker.closest('.ant-select')!);
    fireEvent.change(picker, { target: { value: 'needle' } });
    await waitFor(() => {
      const list = document
        .getElementById(picker.getAttribute('aria-controls')!)!
        .closest('.ant-select-dropdown')!;
      expect(list.querySelectorAll('.ant-select-item-option')).toHaveLength(1);
    });
    expect(picker.closest('.ant-select')!.textContent).toContain('first-owner');
    save();
    await waitFor(() =>
      expect(post.mock.calls.some(([url]) => String(url).endsWith('/update/42'))).toBe(true),
    );
    const payload = post.mock.calls.find(([url]) => String(url).endsWith('/update/42'))![1] as {
      settings: string;
    };
    expect(JSON.parse(payload.settings).accounts[0].ownerClientId).toBe(firstID);
  });

  it('shows list failures and retries on a fresh search while retaining owner UUIDs', async () => {
    const get = vi.mocked(HttpUtil.get);
    get.mockImplementation(async (url) => {
      if (!String(url).includes('/clients/list/paged')) return { success: true, msg: '', obj: [] };
      const recovered = String(url).includes('search=recover');
      return recovered
        ? {
            success: true,
            msg: '',
            obj: {
              items: [{ clientId: secondID, email: 'recovered-owner' }],
              total: 1,
              filtered: 1,
              page: 1,
              pageSize: 25,
            },
          }
        : { success: false, msg: 'unavailable', obj: null };
    });
    renderPassword('http');
    const picker = (await screen.findAllByLabelText('Owner client'))[0];
    await screen.findAllByText('Unable to load clients. Search again to retry.');
    expect(picker.closest('.ant-select')!.textContent).toContain(firstID);
    fireEvent.click(screen.getByRole('tab', { name: 'Protocol' }));
    fireEvent.mouseDown(picker.closest('.ant-select')!);
    fireEvent.change(picker, { target: { value: 'recover' } });
    await screen.findByText('recovered-owner', { selector: '.ant-select-item-option-content' });
    expect(picker.closest('.ant-select')!.textContent).toContain(firstID);
  });

  it('invalidates cached choices after actual standalone client creation', async () => {
    const queryClient = new QueryClient({ defaultOptions: appQueryClient.getDefaultOptions() });
    let created = false;
    vi.mocked(HttpUtil.get).mockImplementation(async (url) => ({
      success: true,
      msg: '',
      obj: String(url).includes('/clients/list/paged')
        ? {
            items: created ? [{ clientId: secondID, email: 'new-standalone-owner' }] : [],
            total: created ? 1 : 0,
            filtered: created ? 1 : 0,
            page: 1,
            pageSize: 25,
          }
        : [],
    }));
    vi.mocked(HttpUtil.post).mockImplementation(async (url) => {
      if (url === '/panel/api/xray/')
        return { success: true, msg: '', obj: JSON.stringify({ xraySetting: { outbounds: [] } }) };
      if (url === '/panel/api/clients/add') created = true;
      return { success: true, msg: '', obj: {} };
    });
    const first = renderPassword('http', { queryClient });
    await waitFor(() => {
      expect(
        queryClient.getQueryCache().findAll({ queryKey: ['clients', 'password-owner-choices'] }),
      ).toHaveLength(1);
      expect(queryClient.isFetching()).toBe(0);
    });
    first.unmount();
    const hook = renderHook(() => useClients({ list: false }), {
      wrapper: ({ children }) => (
        <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
      ),
    });
    await act(async () => {
      await hook.result.current.create({
        client: { email: 'new-standalone-owner' },
        inboundIds: [],
      });
    });
    renderPassword('http', { queryClient });
    fireEvent.mouseDown(
      (await screen.findAllByLabelText('Owner client'))[0].closest('.ant-select')!,
    );
    await screen.findByText('new-standalone-owner', {
      selector: '.ant-select-item-option-content',
    });
    hook.unmount();
    queryClient.clear();
  });
});

import { describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import InboundFormModal from '@/pages/inbounds/form/InboundFormModal';
import { DBInbound } from '@/models/dbinbound';
import { HttpUtil } from '@/utils';
import { chooseSelectOption, renderWithProviders } from './test-utils';

const firstID = 'a6426bfc-42c6-45d6-8182-1108f7986d89';
const secondID = '97af23ee-90d3-48d3-bb9f-790322466bea';

function renderTunnel(remote = false) {
  const inbound = new DBInbound({
    id: 42,
    protocol: 'tunnel',
    port: 24101,
    ownerClientId: firstID,
    nodeId: remote ? 7 : null,
    settings: {
      rewriteAddress: '127.0.0.1',
      rewritePort: 80,
      allowedNetwork: 'tcp,udp',
      clients: [{ email: 'former-owner', enable: true }],
    },
  });
  renderWithProviders(
    <InboundFormModal
      open
      mode="edit"
      dbInbound={inbound}
      dbInbounds={[inbound]}
      availableNodes={[]}
      onClose={() => {}}
      onSaved={() => {}}
    />,
  );
}

function save() {
  const button = document.querySelector('.ant-modal-footer .ant-btn-primary');
  if (!button) throw new Error('missing Save button');
  fireEvent.click(button);
}

function mockOwnerPage() {
  const get = vi.mocked(HttpUtil.get);
  get.mockImplementation(async (url) => {
    if (String(url).includes('/clients/list/paged')) {
      return {
        success: true,
        msg: '',
        obj: {
          items: [{ email: 'selected-owner', clientId: secondID, inboundIds: [] }],
          total: 1,
          filtered: 1,
          page: 1,
          pageSize: 25,
        },
      };
    }
    return { success: true, msg: '', obj: [] };
  });
  return get;
}

describe('Tunnel owner form', () => {
  it('searches and loads further choices without changing the selected account label', async () => {
    const get = vi.mocked(HttpUtil.get);
    get.mockImplementation(async (url) => {
      if (!String(url).includes('/clients/list/paged')) return { success: true, msg: '', obj: [] };
      const params = new URL(String(url), 'http://panel.test').searchParams;
      const page = Number(params.get('page'));
      return {
        success: true,
        msg: '',
        obj: {
          items:
            params.get('search') === 'needle'
              ? []
              : page === 1
                ? [{ clientId: firstID, email: 'former-owner' }]
                : [{ clientId: secondID, email: 'selected-owner' }],
          total: 26,
          filtered: params.get('search') === 'needle' ? 0 : 26,
          page,
          pageSize: 25,
        },
      };
    });
    renderTunnel();
    const picker = await screen.findByLabelText('Owner client');
    fireEvent.mouseDown(picker.closest('.ant-select')!);
    fireEvent.click(await screen.findByRole('button', { name: 'Load more clients' }));
    await screen.findByText('selected-owner', { selector: '.ant-select-item-option-content' });
    chooseSelectOption('ownerClientId', 'selected-owner');
    fireEvent.change(picker, { target: { value: 'needle' } });
    await waitFor(() =>
      expect(get.mock.calls.some(([url]) => String(url).includes('search=needle'))).toBe(true),
    );
    await waitFor(() =>
      expect(
        document.querySelector('#ownerClientId')?.closest('.ant-select')?.textContent,
      ).toContain('selected-owner'),
    );
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
  it('selects an existing account by stable identity without resubmitting its fields', async () => {
    mockOwnerPage();
    const post = vi.mocked(HttpUtil.post);
    post.mockClear();
    renderTunnel();
    const picker = await screen.findByLabelText('Owner client');
    fireEvent.mouseDown(picker.closest('.ant-select')!);
    await screen.findByText('selected-owner', { selector: '.ant-select-item-option-content' });
    chooseSelectOption('ownerClientId', 'selected-owner');
    save();
    await waitFor(() =>
      expect(post.mock.calls.some(([url]) => String(url).endsWith('/update/42'))).toBe(true),
    );
    const payload = post.mock.calls.find(([url]) =>
      String(url).endsWith('/update/42'),
    )![1] as Record<string, unknown>;
    expect(payload.ownerClientId).toBe(secondID);
    expect(JSON.parse(payload.settings as string).clients).toBeUndefined();
    expect(payload.clientStats).toBeUndefined();
  });

  it('requires an owner for a new Tunnel', async () => {
    mockOwnerPage();
    const post = vi.mocked(HttpUtil.post);
    post.mockClear();
    renderWithProviders(
      <InboundFormModal
        open
        mode="add"
        dbInbound={null}
        dbInbounds={[]}
        availableNodes={[]}
        onClose={() => {}}
        onSaved={() => {}}
      />,
    );
    chooseSelectOption('protocol', 'tunnel');
    save();
    await screen.findByText('Select an owner client');
    expect(screen.getByRole('tab', { name: 'Protocol' }).getAttribute('aria-selected')).toBe(
      'true',
    );
    expect(
      screen
        .getByLabelText('Owner client')
        .closest('[role="tabpanel"]')
        ?.getAttribute('aria-hidden'),
    ).toBe('false');
    expect(post.mock.calls.some(([url]) => String(url).endsWith('/inbounds/add'))).toBe(false);
  });

  it('keeps remote ownership read-only and omits the local command', async () => {
    const get = mockOwnerPage();
    get.mockClear();
    const post = vi.mocked(HttpUtil.post);
    post.mockClear();
    renderTunnel(true);
    const picker = await screen.findByLabelText('Owner client');
    expect((picker as HTMLInputElement).disabled).toBe(true);
    expect(get.mock.calls.some(([url]) => String(url).includes('/clients/list/paged'))).toBe(false);
    save();
    await waitFor(() =>
      expect(post.mock.calls.some(([url]) => String(url).endsWith('/update/42'))).toBe(true),
    );
    const payload = post.mock.calls.find(([url]) =>
      String(url).endsWith('/update/42'),
    )![1] as Record<string, unknown>;
    expect(payload.ownerClientId).toBeUndefined();
    expect(JSON.parse(payload.settings as string).clients).toEqual([
      { email: 'former-owner', enable: true },
    ]);
  });
});

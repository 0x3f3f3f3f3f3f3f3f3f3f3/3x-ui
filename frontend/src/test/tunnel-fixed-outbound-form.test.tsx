import { fireEvent, screen, waitFor } from '@testing-library/react';
import { expect, it, vi } from 'vitest';
import { DBInbound } from '@/models/dbinbound';
import InboundFormModal from '@/pages/inbounds/form/InboundFormModal';
import { HttpUtil } from '@/utils';
import { chooseSelectOption, listSelectOptions, renderWithProviders } from './test-utils';

const owner = 'a6426bfc-42c6-45d6-8182-1108f7986d89';

function renderTunnel(outboundTag?: string) {
  const inbound = new DBInbound({
    id: 42,
    protocol: 'tunnel',
    port: 24101,
    ownerClientId: owner,
    settings: {
      clients: [{ email: 'fixed-owner' }],
      rewriteAddress: '127.0.0.1',
      rewritePort: 80,
      outboundTag,
    },
  });
  vi.mocked(HttpUtil.get).mockResolvedValue({
    success: true,
    msg: '',
    obj: {
      items: [{ clientId: owner, email: 'fixed-owner' }],
      page: 1,
      pageSize: 25,
      total: 1,
      filtered: 1,
    },
  });
  const post = vi.mocked(HttpUtil.post);
  post.mockClear();
  post.mockImplementation(async (url) => ({
    success: true,
    msg: '',
    obj:
      url === '/panel/api/xray/'
        ? JSON.stringify({
            xraySetting: {
              outbounds: [
                { tag: 'direct', protocol: 'freedom' },
                { tag: 'block', protocol: 'blackhole' },
              ],
              routing: { balancers: [{ tag: 'balanced', selector: ['direct'] }] },
            },
            subscriptionOutboundTags: ['subscription-proxy'],
          })
        : {},
  }));
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
  return post;
}

async function saveSelection(post: ReturnType<typeof renderTunnel>) {
  const button = document.querySelector('.ant-modal-footer .ant-btn-primary');
  if (!button) throw new Error('missing Save button');
  fireEvent.click(button);
  await waitFor(() =>
    expect(post.mock.calls.some(([url]) => String(url).endsWith('/update/42'))).toBe(true),
  );
  const payload = post.mock.calls.find(([url]) => String(url).endsWith('/update/42'))![1] as Record<
    string,
    unknown
  >;
  expect(payload.ownerClientId).toBe(owner);
  return JSON.parse(payload.settings as string) as Record<string, unknown>;
}

it('selects a concrete subscription outbound while keeping routing balancers out of the fixed picker', async () => {
  const post = renderTunnel();
  await screen.findByLabelText('Fixed outbound');
  await waitFor(() =>
    expect(listSelectOptions('tunnelOutboundTag')).toContain('subscription-proxy'),
  );
  expect(listSelectOptions('tunnelOutboundTag')).toEqual(['direct', 'block', 'subscription-proxy']);
  chooseSelectOption('tunnelOutboundTag', 'subscription-proxy');
  expect((await saveSelection(post)).outboundTag).toBe('subscription-proxy');
});

it('keeps an unavailable saved selection visible and preserves it on save', async () => {
  const post = renderTunnel('removed-proxy');
  await screen.findByText('removed-proxy (unavailable)');
  expect((await saveSelection(post)).outboundTag).toBe('removed-proxy');
});

it('explicitly clears the fixed selection to return to normal routing', async () => {
  const post = renderTunnel('direct');
  const input = await screen.findByLabelText('Fixed outbound');
  const clear = input.closest('.ant-select')?.querySelector('.ant-select-clear');
  if (!clear) throw new Error('missing outbound clear control');
  fireEvent.click(clear);
  expect((await saveSelection(post)).outboundTag).toBeUndefined();
});

it('displays routing mode for a persisted empty selection', async () => {
  renderTunnel('');
  await screen.findByText('Use routing rules');
});

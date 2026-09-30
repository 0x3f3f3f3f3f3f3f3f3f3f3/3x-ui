import { fireEvent, screen, waitFor } from '@testing-library/react';
import { expect, it, vi } from 'vitest';
import { DBInbound } from '@/models/dbinbound';
import InboundFormModal from '@/pages/inbounds/form/InboundFormModal';
import { HttpUtil } from '@/utils';
import { renderWithProviders } from './test-utils';

it('edits source CIDRs and sends them with the selected canonical owner', async () => {
  const owner = 'a6426bfc-42c6-45d6-8182-1108f7986d89';
  const inbound = new DBInbound({
    id: 42,
    protocol: 'tunnel',
    port: 24101,
    ownerClientId: owner,
    settings: {
      clients: [{ email: 'acl-owner' }],
      allowedSourceCidrs: ['192.0.2.0/24'],
      rewriteAddress: '127.0.0.1',
      rewritePort: 80,
    },
  });
  vi.mocked(HttpUtil.get).mockResolvedValue({
    success: true,
    msg: '',
    obj: {
      items: [{ clientId: owner, email: 'acl-owner' }],
      page: 1,
      pageSize: 25,
      total: 1,
      filtered: 1,
    },
  });
  const post = vi.mocked(HttpUtil.post);
  post.mockClear();
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
  const sources = await screen.findByLabelText('Allowed source CIDRs');
  fireEvent.change(sources, { target: { value: '2001:db8::/32' } });
  fireEvent.keyDown(sources, { key: 'Enter', code: 'Enter', keyCode: 13 });
  const save = document.querySelector('.ant-modal-footer .ant-btn-primary');
  if (!save) throw new Error('missing Save button');
  fireEvent.click(save);
  await waitFor(() =>
    expect(post.mock.calls.some(([url]) => String(url).endsWith('/update/42'))).toBe(true),
  );
  const payload = post.mock.calls.find(([url]) => String(url).endsWith('/update/42'))![1] as Record<
    string,
    unknown
  >;
  expect(JSON.parse(payload.settings as string).allowedSourceCidrs).toEqual([
    '192.0.2.0/24',
    '2001:db8::/32',
  ]);
  expect(payload.ownerClientId).toBe(owner);
});

import { describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import ClientBulkAddModal from '@/pages/clients/ClientBulkAddModal';
import type { InboundOption } from '@/schemas/client';
import { renderWithProviders } from './test-utils';

const { bulkCreate } = vi.hoisted(() => ({
  bulkCreate: vi.fn().mockResolvedValue({ success: true }),
}));
vi.mock('@/hooks/useClients', () => ({ useClients: () => ({ bulkCreate }) }));

describe('native mieru bulk attachment', () => {
  it('offers the native listener and submits a client record for server credential generation', async () => {
    renderWithProviders(
      <ClientBulkAddModal
        open
        inbounds={[
          {
            id: 9,
            protocol: 'mieru',
            port: 8443,
            remark: 'native listener',
            enable: true,
          } as InboundOption,
        ]}
        onOpenChange={() => {}}
      />,
    );
    const select = document.querySelector('.ant-select')!;
    fireEvent.mouseDown(select.querySelector('.ant-select-selector') ?? select);
    const options = Array.from(document.querySelectorAll('.ant-select-item-option'));
    expect(options).toHaveLength(1);
    fireEvent.click(options[0]);
    fireEvent.click(await screen.findByRole('button', { name: /create/i }));
    await waitFor(() => expect(bulkCreate).toHaveBeenCalledTimes(1));
    const batch = bulkCreate.mock.calls[0][0];
    expect(batch).toHaveLength(1);
    expect(batch[0].inboundIds).toEqual([9]);
    expect(batch[0].client.email).not.toBe('');
    expect(batch[0].client.mieruUsername).toBe('');
    expect(batch[0].client.mieruPassword).toBe('');
  });
});

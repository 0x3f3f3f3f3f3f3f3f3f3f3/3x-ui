import { describe, expect, it, vi } from 'vitest';
import { useState } from 'react';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import ClientBulkAddModal from '@/pages/clients/ClientBulkAddModal';
import BulkAttachInboundsModal from '@/pages/clients/BulkAttachInboundsModal';
import type { InboundOption } from '@/schemas/client';
import { renderWithProviders } from './test-utils';
const { bulkCreate } = vi.hoisted(() => ({
  bulkCreate: vi.fn().mockResolvedValue({ success: true }),
}));
vi.mock('@/hooks/useClients', () => ({ useClients: () => ({ bulkCreate }) }));
const empty = {
  id: 19,
  protocol: 'snell',
  port: 443,
  remark: 'Snell free',
  tag: 'snell-free',
  snellVersion: 6,
  snellOwnerCount: 0,
} as InboundOption;
function select(label: string) {
  const control = document.querySelector('.ant-select')!;
  fireEvent.mouseDown(control.querySelector('.ant-select-selector') ?? control);
  const option = Array.from(document.querySelectorAll('.ant-select-item-option')).find((el) =>
    el.textContent?.includes(label),
  );
  expect(option).toBeTruthy();
  fireEvent.click(option!);
}
function ChangingSelection({ submit }: { submit: (ids: number[]) => Promise<null> }) {
  const [count, setCount] = useState(1);
  return (
    <>
      <button onClick={() => setCount(2)}>Select two accounts</button>
      <BulkAttachInboundsModal
        open
        count={count}
        inbounds={[empty]}
        onSubmit={submit}
        onOpenChange={() => {}}
      />
    </>
  );
}
describe('Snell exclusive owner bulk forms', () => {
  it.each(['ssh', 'mieru'])(
    'preserves %s as a multi-account attachment target',
    async (protocol) => {
      const submit = vi.fn().mockResolvedValue(null);
      renderWithProviders(
        <BulkAttachInboundsModal
          open
          count={2}
          inbounds={[{ ...empty, protocol }]}
          onSubmit={submit}
          onOpenChange={() => {}}
        />,
      );
      select('Snell free');
      fireEvent.click(document.querySelector('.ant-modal-footer .ant-btn-primary')!);
      await waitFor(() => expect(submit).toHaveBeenCalledWith([19]));
    },
  );
  it('creates one owner and delegates an omitted PSK to the canonical writer', async () => {
    renderWithProviders(<ClientBulkAddModal open inbounds={[empty]} onOpenChange={() => {}} />);
    select('Snell free');
    const quantity = Array.from(document.querySelectorAll('.ant-form-item'))
      .find(
        (el) =>
          el.querySelector('.ant-form-item-label label')?.textContent?.trim() ===
          'Number of Clients',
      )!
      .querySelector('input')!;
    expect(quantity.value).toBe('1');
    expect(quantity.disabled).toBe(true);
    fireEvent.click(await screen.findByRole('button', { name: /create/i }));
    await waitFor(() => expect(bulkCreate).toHaveBeenCalledTimes(1));
    expect(bulkCreate.mock.calls[0][0]).toHaveLength(1);
    expect(bulkCreate.mock.calls[0][0][0]).toMatchObject({
      inboundIds: [19],
      client: { snellPsk: '' },
    });
  });
  it('reserves a listener occupied by a disabled owner', async () => {
    renderWithProviders(
      <ClientBulkAddModal
        open
        inbounds={[{ ...empty, snellOwnerCount: 1, snellOwnerClientId: 'disabled-owner' }]}
        onOpenChange={() => {}}
      />,
    );
    const control = document.querySelector('.ant-select')!;
    fireEvent.mouseDown(control.querySelector('.ant-select-selector') ?? control);
    const option = Array.from(document.querySelectorAll('.ant-select-item-option')).find((el) =>
      el.textContent?.includes('Snell free'),
    );
    expect(option?.classList.contains('ant-select-item-option-disabled')).toBe(true);
    expect(bulkCreate).not.toHaveBeenCalled();
  });
  it('offers a free listener for a single account attachment', async () => {
    const submit = vi.fn().mockResolvedValue({ attached: [{}], skipped: [], errors: [] });
    renderWithProviders(
      <BulkAttachInboundsModal
        open
        count={1}
        inbounds={[empty]}
        onSubmit={submit}
        onOpenChange={() => {}}
      />,
    );
    select('Snell free');
    fireEvent.click(document.querySelector('.ant-modal-footer .ant-btn-primary')!);
    await waitFor(() => expect(submit).toHaveBeenCalledWith([19]));
  });
  it('offers no Snell target for more than one account', () => {
    renderWithProviders(
      <BulkAttachInboundsModal
        open
        count={2}
        inbounds={[empty]}
        onSubmit={vi.fn()}
        onOpenChange={() => {}}
      />,
    );
    expect(document.querySelector('.ant-select')).toBeNull();
  });
  it('invalidates a selected Snell target when the account count changes', async () => {
    const submit = vi.fn().mockResolvedValue(null);
    renderWithProviders(<ChangingSelection submit={submit} />);
    select('Snell free');
    fireEvent.click(screen.getByRole('button', { name: 'Select two accounts' }));
    const button = document.querySelector<HTMLButtonElement>('.ant-modal-footer .ant-btn-primary')!;
    expect(button.disabled).toBe(true);
    fireEvent.click(button);
    expect(submit).not.toHaveBeenCalled();
  });
});

import { describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import ClientBulkAddModal from '@/pages/clients/ClientBulkAddModal';
import type { InboundOption } from '@/schemas/client';
import { renderWithProviders } from './test-utils';
const { bulkCreate } = vi.hoisted(() => ({
  bulkCreate: vi.fn().mockResolvedValue({ success: true }),
}));
vi.mock('@/hooks/useClients', () => ({ useClients: () => ({ bulkCreate }) }));
const KEY =
  'ssh-ed25519 ' +
  btoa(
    String.fromCharCode(0, 0, 0, 11) +
      'ssh-ed25519' +
      String.fromCharCode(0, 0, 0, 32) +
      'k'.repeat(32),
  );
describe('native SSH bulk attachment', () => {
  it('requires user-supplied public-key authentication for a key-only listener', async () => {
    renderWithProviders(
      <ClientBulkAddModal
        open
        inbounds={[
          {
            id: 9,
            protocol: 'ssh',
            port: 2222,
            remark: 'SSH listener',
            sshAllowPassword: false,
          } as InboundOption,
        ]}
        onOpenChange={() => {}}
      />,
    );
    const select = document.querySelector('.ant-select')!;
    fireEvent.mouseDown(select.querySelector('.ant-select-selector') ?? select);
    const option = Array.from(document.querySelectorAll('.ant-select-item-option')).find((el) =>
      el.textContent?.includes('SSH listener'),
    );
    expect(option).toBeTruthy();
    fireEvent.click(option!);
    fireEvent.click(await screen.findByRole('button', { name: /create/i }));
    await screen.findByText(
      'Provide SSH public keys, or a password accepted by every selected SSH listener.',
    );
    expect(bulkCreate).not.toHaveBeenCalled();
    fireEvent.change(await screen.findByLabelText('SSH authorized public keys'), {
      target: { value: KEY },
    });
    fireEvent.click(await screen.findByRole('button', { name: /create/i }));
    await waitFor(() => expect(bulkCreate).toHaveBeenCalledTimes(1));
    expect(bulkCreate.mock.calls[0][0][0]).toMatchObject({
      inboundIds: [9],
      client: { sshUsername: '', sshAuthorizedKeys: KEY, sshPassword: '' },
    });
  });
  it('generates independent passwords only after opting in on a password-enabled listener', async () => {
    renderWithProviders(
      <ClientBulkAddModal
        open
        inbounds={[
          {
            id: 9,
            protocol: 'ssh',
            port: 2222,
            remark: 'SSH password listener',
            sshAllowPassword: true,
          } as InboundOption,
        ]}
        onOpenChange={() => {}}
      />,
    );
    const select = document.querySelector('.ant-select')!;
    fireEvent.mouseDown(select.querySelector('.ant-select-selector') ?? select);
    fireEvent.click(
      Array.from(document.querySelectorAll('.ant-select-item-option')).find((el) =>
        el.textContent?.includes('SSH password listener'),
      )!,
    );
    fireEvent.click(await screen.findByRole('button', { name: /create/i }));
    await screen.findByText(
      'Provide SSH public keys, or a password accepted by every selected SSH listener.',
    );
    expect(bulkCreate).not.toHaveBeenCalled();
    fireEvent.click(
      await screen.findByRole('switch', {
        name: 'Generate an independent SSH password for each account',
      }),
    );
    const quantity = Array.from(document.querySelectorAll('.ant-form-item'))
      .find(
        (el) =>
          el.querySelector('.ant-form-item-label label')?.textContent?.trim() ===
          'Number of Clients',
      )!
      .querySelector('input')!;
    fireEvent.change(quantity, { target: { value: '2' } });
    fireEvent.click(await screen.findByRole('button', { name: /create/i }));
    await waitFor(() => expect(bulkCreate).toHaveBeenCalledTimes(1));
    const batch = bulkCreate.mock.calls[0][0];
    expect(batch).toHaveLength(2);
    expect(batch[0].client.sshPassword).toMatch(/^[a-f0-9]{32}$/);
    expect(batch[1].client.sshPassword).not.toBe(batch[0].client.sshPassword);
    expect(batch[0].client.sshPassword).not.toBe(batch[0].client.password);
    expect(
      batch.every((row: { client: { sshUsername: string } }) => row.client.sshUsername === ''),
    ).toBe(true);
  });
});

import { ConfigProvider } from 'antd';
import { describe, it, expect, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import ClientFormModal from '@/pages/clients/ClientFormModal';
import type { ClientRecord, InboundOption } from '@/schemas/client';
import { renderWithProviders } from './test-utils';

const publicKey =
  'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMlM7FlZhqPVGX/CZdQJcMWTCfcRWAXXrgNKWBGPVH4L';
const local: InboundOption = { id: 18, protocol: 'mieru', tag: 'mieru-local', enable: true };

function renderMieru(inbounds: InboundOption[] = [local], client: ClientRecord | null = null) {
  const save = vi.fn().mockResolvedValue({ success: false });
  renderWithProviders(
    <ConfigProvider virtual={false}>
      <ClientFormModal
        open
        mode={client ? 'edit' : 'add'}
        client={client}
        inbounds={inbounds}
        attachedIds={client ? [18] : []}
        save={save}
        onOpenChange={() => {}}
      />
    </ConfigProvider>,
  );
  if (!client) fireEvent.click(screen.getByRole('button', { name: 'Select all' }));
  fireEvent.click(screen.getByRole('tab', { name: 'Credentials' }));
  return save;
}

function passwordInput() {
  const label = screen.getByText('Password', { selector: 'label' });
  const input = label.closest('.ant-form-item')?.querySelector('input');
  if (!input) throw new Error('Password input missing');
  return input;
}

describe('mieru canonical client editing', () => {
  it('creates a native password client attached to the chosen local listener', async () => {
    const save = renderMieru();
    fireEvent.change(passwordInput(), { target: { value: 'fixture-mieru-password' } });
    expect(screen.queryByText('UUID', { selector: 'label' })).toBeNull();
    expect(screen.queryByText('Hysteria Auth', { selector: 'label' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0]?.[0]).toMatchObject({
      inboundIds: [18],
      client: {
        password: 'fixture-mieru-password',
        enable: true,
        totalGB: 0,
        expiryTime: 0,
      },
    });
  });

  it('creates one client across local SSH and mieru with both credentials', async () => {
    const save = renderMieru([{ id: 17, protocol: 'ssh', tag: 'ssh-local', enable: true }, local]);
    fireEvent.change(await screen.findByLabelText('SSH public keys'), {
      target: { value: publicKey },
    });
    fireEvent.change(passwordInput(), { target: { value: 'shared-password' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0]?.[0]).toMatchObject({
      inboundIds: [17, 18],
      client: {
        password: 'shared-password',
        ssh: { publicKeys: [publicKey], targets: [], reverse: [] },
      },
    });
  });

  it.each(['', 'a'.repeat(65), '界'.repeat(22)])(
    'rejects invalid native password %j',
    async (password) => {
      const save = renderMieru();
      fireEvent.change(passwordInput(), { target: { value: password } });
      fireEvent.click(screen.getByRole('button', { name: 'Create' }));
      await screen.findByText('mieru passwords must contain 1–64 UTF-8 bytes.');
      expect(save).not.toHaveBeenCalled();
    },
  );

  it('rotates the password without losing lifecycle or renewal state', async () => {
    const save = renderMieru([local], {
      email: 'alice',
      password: 'old-password',
      subId: 'alice-sub',
      enable: false,
      totalGB: 1234,
      expiryTime: -86400000,
      resetWeekday: 3,
      resetMax: 4,
      group: 'group-a',
      comment: 'client note',
      trafficReset: 'monthly',
      trafficResetDay: 9,
    });
    fireEvent.change(passwordInput(), { target: { value: 'new-password' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0]?.[0]).toMatchObject({
      email: 'alice',
      password: 'new-password',
      subId: 'alice-sub',
      enable: false,
      totalGB: 1234,
      expiryTime: -86400000,
      resetWeekday: 3,
      resetMax: 4,
      group: 'group-a',
      comment: 'client note',
      trafficReset: 'monthly',
      trafficResetDay: 9,
    });
  });

  it('does not offer unsupported remote attachments for a mieru client', async () => {
    const save = renderMieru([local, { ...local, id: 19, nodeId: 7, tag: 'mieru-remote' }]);
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0]?.[0]).toMatchObject({ inboundIds: [18] });
  });
});

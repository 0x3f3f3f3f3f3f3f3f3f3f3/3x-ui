import { describe, it, expect, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import ClientFormModal from '@/pages/clients/ClientFormModal';
import type { ClientRecord } from '@/schemas/client';
import { renderWithProviders } from './test-utils';

const publicKey =
  'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMlM7FlZhqPVGX/CZdQJcMWTCfcRWAXXrgNKWBGPVH4L';
const inbounds = [{ id: 17, protocol: 'ssh', tag: 'ssh-local', enable: true }];

function renderSSH(client: ClientRecord | null = null) {
  const save = vi.fn().mockResolvedValue({ success: false });
  renderWithProviders(
    <ClientFormModal
      open
      mode={client ? 'edit' : 'add'}
      client={client}
      inbounds={inbounds}
      attachedIds={client ? [17] : []}
      save={save}
      onOpenChange={() => {}}
    />,
  );
  if (!client) fireEvent.click(screen.getByRole('button', { name: 'Select all' }));
  fireEvent.click(screen.getByRole('tab', { name: 'Credentials' }));
  return save;
}

describe('SSH client credentials and permissions', () => {
  it('creates a public-key client with explicit targets and reverse forwarding off', async () => {
    const save = renderSSH();
    fireEvent.change(await screen.findByLabelText('SSH public keys'), {
      target: { value: publicKey },
    });
    expect(screen.queryByText('UUID', { selector: 'label' })).toBeNull();
    expect(screen.queryByText('Password', { selector: 'label' })).toBeNull();
    expect(screen.queryByText('Hysteria Auth', { selector: 'label' })).toBeNull();
    expect(screen.queryByText('IP Limit', { selector: 'label' })).toBeNull();
    expect(screen.queryByText('HWID Limit', { selector: 'label' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: /Add target/ }));
    fireEvent.change(screen.getByLabelText('Target host'), { target: { value: 'route.example' } });
    fireEvent.change(screen.getByLabelText('Target port'), { target: { value: '443' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0]?.[0]).toMatchObject({
      inboundIds: [17],
      client: {
        ssh: {
          publicKeys: [publicKey],
          targets: [{ host: 'route.example', port: 443 }],
          reverse: [],
        },
        totalGB: 0,
        expiryTime: 0,
        enable: true,
      },
    });
  });

  it('preserves stored access rules and quota when changing the public key comment', async () => {
    const save = renderSSH({
      email: 'alice',
      subId: 'alice-sub',
      enable: false,
      totalGB: 1234,
      expiryTime: -86400000,
      resetWeekday: 3,
      ssh: {
        publicKeys: [publicKey],
        targets: [{ host: '*', port: 0 }],
        reverse: [{ address: '::1', port: 0 }],
      },
    });
    const keys = await screen.findByLabelText('SSH public keys');
    expect((keys as HTMLTextAreaElement).value).toBe(publicKey);
    fireEvent.change(keys, { target: { value: `${publicKey} laptop` } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0]?.[0]).toMatchObject({
      email: 'alice',
      subId: 'alice-sub',
      enable: false,
      totalGB: 1234,
      expiryTime: -86400000,
      resetWeekday: 3,
      ssh: {
        publicKeys: [`${publicKey} laptop`],
        targets: [{ host: '*', port: 0 }],
        reverse: [{ address: '::1', port: 0 }],
      },
    });
  });

  it('rejects missing credentials and private keys before submitting', async () => {
    const save = renderSSH();
    const keys = await screen.findByLabelText('SSH public keys');
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));
    await screen.findByText('Add at least one SSH public key.');
    expect(save).not.toHaveBeenCalled();
    fireEvent.change(keys, { target: { value: '-----BEGIN OPENSSH PRIVATE KEY-----' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));
    await screen.findByText(
      'Enter an SSH public key, without authorized_keys options or certificates.',
    );
    expect(save).not.toHaveBeenCalled();
  });
});

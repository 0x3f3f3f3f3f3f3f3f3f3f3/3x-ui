import { useState } from 'react';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import ClientBulkAddModal from '@/pages/clients/ClientBulkAddModal';
import type { InboundOption } from '@/schemas/client';
import { renderWithProviders } from './test-utils';

const { bulkCreate } = vi.hoisted(() => ({
  bulkCreate: vi.fn().mockResolvedValue({ success: true, obj: { created: 2, skipped: [] } }),
}));
vi.mock('@/hooks/useClients', () => ({ useClients: () => ({ bulkCreate }) }));

const local: InboundOption = {
  id: 9,
  protocol: 'mieru',
  port: 8443,
  tag: 'local-policy',
  enable: true,
};

function openForm(rows: InboundOption[] = [local]) {
  renderWithProviders(<ClientBulkAddModal open inbounds={rows} onOpenChange={() => {}} />);
}

function selectAll() {
  fireEvent.click(screen.getByRole('button', { name: 'Select all' }));
}

function setPolicy(multiplier = '1.234567') {
  fireEvent.change(screen.getByRole('spinbutton', { name: /^Upload limit/ }), {
    target: { value: '262144' },
  });
  fireEvent.change(screen.getByRole('spinbutton', { name: /^Download limit/ }), {
    target: { value: '1048576' },
  });
  fireEvent.change(screen.getByRole('textbox', { name: /^Billing multiplier/ }), {
    target: { value: multiplier },
  });
}

async function submit() {
  fireEvent.click(screen.getByRole('button', { name: /^Create$/i }));
  await waitFor(() => expect(bulkCreate).toHaveBeenCalledTimes(1));
  return bulkCreate.mock.calls[0][0];
}

describe('bulk shared client policy', () => {
  it('sends the exact policy for each independently generated native account', async () => {
    openForm();
    selectAll();
    fireEvent.change(screen.getByRole('spinbutton', { name: 'Number of Clients' }), {
      target: { value: '2' },
    });
    setPolicy();
    const batch = await submit();
    expect(batch).toHaveLength(2);
    for (const item of batch) {
      expect(item.inboundIds).toEqual([9]);
      expect(item.client.policy).toEqual({
        uploadBytesPerSecond: 262144,
        downloadBytesPerSecond: 1048576,
        multiplier: '1.234567',
      });
      expect(item.client.mieruUsername).toBe('');
      expect(item.client.mieruPassword).toBe('');
    }
    expect(batch[0].client.id).not.toBe(batch[1].client.id);
    expect(batch[0].client.password).not.toBe(batch[1].client.password);
    expect(batch[0].client.policy).not.toBe(batch[1].client.policy);
  });

  it('preserves Snell exclusive ownership and SSH credentials with a shared policy', async () => {
    openForm([
      local,
      { ...local, id: 10, protocol: 'ssh', sshAllowPassword: true },
      { ...local, id: 11, protocol: 'snell', snellOwnerCount: 0, snellVersion: 6 },
    ]);
    selectAll();
    fireEvent.click(
      screen.getByRole('switch', { name: 'Generate an independent SSH password for each account' }),
    );
    setPolicy('2');
    const batch = await submit();
    expect(batch).toHaveLength(1);
    expect(batch[0].inboundIds).toEqual([9, 10, 11]);
    expect(batch[0].client.policy.multiplier).toBe('2');
    expect(batch[0].client.snellPsk).toBe('');
    expect(batch[0].client.sshPassword).toMatch(/^[a-f0-9]{32}$/);
    expect(batch[0].client.sshPassword).not.toBe(batch[0].client.password);
  });

  it.each([undefined, 7])('keeps blank settings omitted for legacy scope %s', async (nodeId) => {
    openForm([{ ...local, nodeId }]);
    selectAll();
    const batch = await submit();
    expect(batch[0].client).not.toHaveProperty('policy');
  });

  it('accepts explicit unlimited rates without making the multiplier numeric', async () => {
    openForm();
    selectAll();
    fireEvent.change(screen.getByRole('spinbutton', { name: /^Upload limit/ }), {
      target: { value: '0' },
    });
    fireEvent.change(screen.getByRole('textbox', { name: /^Billing multiplier/ }), {
      target: { value: '1' },
    });
    const batch = await submit();
    expect(batch[0].client.policy).toEqual({
      uploadBytesPerSecond: 0,
      downloadBytesPerSecond: 0,
      multiplier: '1',
    });
  });

  it('refuses edited policy when a remote listener is subsequently selected', async () => {
    openForm([{ ...local, nodeId: 7 }]);
    setPolicy('2');
    selectAll();
    expect(
      (screen.getByRole('textbox', { name: /^Billing multiplier/ }) as HTMLInputElement).disabled,
    ).toBe(true);
    fireEvent.click(screen.getByRole('button', { name: /^Create$/i }));
    await waitFor(() =>
      expect(screen.getAllByText(/Traffic policy currently requires local listeners/).length).toBe(
        2,
      ),
    );
    expect(bulkCreate).not.toHaveBeenCalled();
  });

  it('refuses edited policy if a selected listener disappears before submission', async () => {
    function ChangingListeners() {
      const [rows, setRows] = useState([local]);
      return (
        <>
          <button onClick={() => setRows([])}>Remove listener</button>
          <ClientBulkAddModal open inbounds={rows} onOpenChange={() => {}} />
        </>
      );
    }
    renderWithProviders(<ChangingListeners />);
    selectAll();
    setPolicy('2');
    fireEvent.click(screen.getByRole('button', { name: 'Remove listener' }));
    fireEvent.click(screen.getByRole('button', { name: /^Create$/i }));
    await waitFor(() =>
      expect(screen.getAllByText(/Traffic policy currently requires local listeners/).length).toBe(
        2,
      ),
    );
    expect(bulkCreate).not.toHaveBeenCalled();
  });

  it.each(['0', '1e2', '1.1234567'])(
    'refuses unsupported multiplier %s before creating accounts',
    async (multiplier) => {
      openForm();
      selectAll();
      setPolicy(multiplier);
      fireEvent.click(screen.getByRole('button', { name: /^Create$/i }));
      await screen.findByText(/Enter a multiplier greater than 0 and no more than 1000/);
      expect(bulkCreate).not.toHaveBeenCalled();
    },
  );
});

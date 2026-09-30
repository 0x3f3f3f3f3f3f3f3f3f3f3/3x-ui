import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { describe, expect, it, vi } from 'vitest';

import { ThemeProvider } from '@/hooks/useTheme';
import ClientFormModal from '@/pages/clients/ClientFormModal';
import { ClientRecordSchema } from '@/schemas/client';

function openPolicyForm(
  policy?: Record<string, unknown>,
  mode: 'add' | 'edit' = 'edit',
  bindings?: { nodeId?: number; attachedIds?: number[] },
) {
  const save = vi.fn().mockResolvedValue({ success: true });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <ThemeProvider>
      <QueryClientProvider client={qc}>
        <ClientFormModal
          open
          mode={mode}
          client={
            mode === 'add'
              ? null
              : ClientRecordSchema.parse({
                  email: 'shared-policy',
                  uuid: '11111111-1111-1111-1111-111111111111',
                  enable: true,
                  policy,
                })
          }
          attachedIds={bindings?.attachedIds}
          inbounds={[
            {
              id: 19,
              port: 24443,
              protocol: 'vless',
              tag: 'policy-listener',
              enable: true,
              nodeId: bindings?.nodeId,
            },
          ]}
          save={save}
          onOpenChange={() => {}}
        />
      </QueryClientProvider>
    </ThemeProvider>,
  );
  return save;
}

async function submitPolicyForm(save: ReturnType<typeof vi.fn>) {
  fireEvent.click(await screen.findByRole('button', { name: /^save$/i }));
  await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
  return save.mock.calls[0][0] as Record<string, unknown>;
}

describe('client shared traffic policy form', () => {
  it('creates a legacy remote client without introducing policy fields', async () => {
    const save = openPolicyForm(undefined, 'add', { nodeId: 7 });
    fireEvent.click(screen.getByRole('button', { name: 'Select all' }));
    fireEvent.click(screen.getByRole('button', { name: /^create$/i }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    const payload = save.mock.calls[0][0] as {
      client: Record<string, unknown>;
      inboundIds: number[];
    };
    expect(payload.client).not.toHaveProperty('policy');
    expect(payload.inboundIds).toEqual([19]);
  });

  it('rejects adding a remote binding to an existing explicit policy', async () => {
    const save = openPolicyForm(
      { uploadBytesPerSecond: 0, downloadBytesPerSecond: 0, multiplier: '1' },
      'edit',
      { nodeId: 7 },
    );
    fireEvent.click(screen.getByRole('button', { name: 'Select all' }));
    fireEvent.click(screen.getByRole('button', { name: /^save$/i }));
    await waitFor(() =>
      expect(
        screen.getAllByText(/Traffic policy currently requires local listeners/).length,
      ).toBeGreaterThan(1),
    );
    expect(save).not.toHaveBeenCalled();
  });

  it.each([{ nodeId: 7, attachedIds: [19] }, { attachedIds: [42] }])(
    'shows stored policy read-only for remote or unknown bindings: %j',
    async (bindings) => {
      const save = openPolicyForm(
        { uploadBytesPerSecond: 1024, downloadBytesPerSecond: 2048, multiplier: '2' },
        'edit',
        bindings,
      );
      expect(
        (screen.getByRole('spinbutton', { name: /^Upload limit/ }) as HTMLInputElement).disabled,
      ).toBe(true);
      expect(
        (screen.getByRole('spinbutton', { name: /^Download limit/ }) as HTMLInputElement).disabled,
      ).toBe(true);
      expect(
        (screen.getByRole('textbox', { name: /^Billing multiplier/ }) as HTMLInputElement).disabled,
      ).toBe(true);
      expect(
        (screen.getByRole('textbox', { name: /^Billing multiplier/ }) as HTMLInputElement).value,
      ).toBe('2');
      expect(screen.getByText(/Traffic policy currently requires local listeners/)).not.toBeNull();
      expect(await submitPolicyForm(save)).not.toHaveProperty('policy');
    },
  );

  it('keeps policy disabled after a remote binding is deselected in the same save', async () => {
    const save = openPolicyForm(undefined, 'edit', { nodeId: 7, attachedIds: [19] });
    fireEvent.click(screen.getByRole('button', { name: 'Clear all' }));
    expect(
      (screen.getByRole('textbox', { name: /^Billing multiplier/ }) as HTMLInputElement).disabled,
    ).toBe(true);
    expect(await submitPolicyForm(save)).not.toHaveProperty('policy');
  });

  it('does not silently discard edited policy when a remote listener is then selected', async () => {
    const save = openPolicyForm(undefined, 'add', { nodeId: 7 });
    fireEvent.change(screen.getByRole('textbox', { name: /^Billing multiplier/ }), {
      target: { value: '2' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Select all' }));
    expect(
      (screen.getByRole('textbox', { name: /^Billing multiplier/ }) as HTMLInputElement).disabled,
    ).toBe(true);
    fireEvent.click(screen.getByRole('button', { name: /^create$/i }));
    await waitFor(() =>
      expect(
        screen.getAllByText(/Traffic policy currently requires local listeners/).length,
      ).toBeGreaterThan(1),
    );
    expect(save).not.toHaveBeenCalled();
  });

  it('creates a new client with the selected listener and explicit default traffic policy', async () => {
    const save = openPolicyForm(undefined, 'add');
    fireEvent.click(screen.getByRole('button', { name: 'Select all' }));
    fireEvent.change(screen.getByRole('textbox', { name: /^Billing multiplier/ }), {
      target: { value: '1' },
    });
    fireEvent.click(screen.getByRole('button', { name: /^create$/i }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    const payload = save.mock.calls[0][0] as {
      client: Record<string, unknown>;
      inboundIds: number[];
    };
    expect(payload.client.policy).toEqual({
      uploadBytesPerSecond: 0,
      downloadBytesPerSecond: 0,
      multiplier: '1',
    });
    expect(payload.inboundIds).toEqual([19]);
  });

  it('rejects excessive multiplier precision and permits the smallest supported multiplier after correction', async () => {
    const save = openPolicyForm();
    const multiplier = screen.getByRole('textbox', { name: /^Billing multiplier/ });
    fireEvent.change(multiplier, { target: { value: '0.1234567' } });
    fireEvent.click(await screen.findByRole('button', { name: /^save$/i }));
    await screen.findByText(
      'Enter a multiplier greater than 0 and no more than 1000, with up to 6 decimal places.',
    );
    expect(save).not.toHaveBeenCalled();
    fireEvent.change(multiplier, { target: { value: '0.000001' } });
    expect((await submitPolicyForm(save)).policy).toEqual({
      uploadBytesPerSecond: 0,
      downloadBytesPerSecond: 0,
      multiplier: '0.000001',
    });
  });

  it('submits edited rates and an exact decimal multiplier through the existing save boundary', async () => {
    const save = openPolicyForm();
    fireEvent.change(screen.getByRole('spinbutton', { name: /^Upload limit/ }), {
      target: { value: '4096' },
    });
    fireEvent.change(screen.getByRole('spinbutton', { name: /^Download limit/ }), {
      target: { value: '8192' },
    });
    fireEvent.change(screen.getByRole('textbox', { name: /^Billing multiplier/ }), {
      target: { value: '0.123456' },
    });
    expect((await submitPolicyForm(save)).policy).toEqual({
      uploadBytesPerSecond: 4096,
      downloadBytesPerSecond: 8192,
      multiplier: '0.123456',
    });
  });

  it('preserves existing policy values on an unrelated save', async () => {
    const policy = {
      uploadBytesPerSecond: 12345,
      downloadBytesPerSecond: 0,
      multiplier: '2.000001',
    };
    const save = openPolicyForm(policy);
    expect((await submitPolicyForm(save)).policy).toEqual(policy);
  });

  it('keeps legacy clients without explicit policy fields unchanged', async () => {
    const save = openPolicyForm();
    expect(await submitPolicyForm(save)).not.toHaveProperty('policy');
  });

  it('restores unlimited rates and multiplier one when existing overrides are cleared', async () => {
    const save = openPolicyForm({
      uploadBytesPerSecond: 1024,
      downloadBytesPerSecond: 2048,
      multiplier: '2',
    });
    for (const name of [/^Upload limit/, /^Download limit/]) {
      fireEvent.change(screen.getByRole('spinbutton', { name }), { target: { value: '' } });
    }
    fireEvent.change(screen.getByRole('textbox', { name: /^Billing multiplier/ }), {
      target: { value: '' },
    });
    expect((await submitPolicyForm(save)).policy).toEqual({
      uploadBytesPerSecond: 0,
      downloadBytesPerSecond: 0,
      multiplier: '',
    });
  });
});

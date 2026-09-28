import { beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor } from '@testing-library/react';

import ClientFormModal from '@/pages/clients/ClientFormModal';
import { HttpUtil, Msg } from '@/utils';
import { keys } from '@/api/queryKeys';
import { makeTestQueryClient, renderWithProviders } from './test-utils';

const initialPolicy = {
  policyId: '11111111-1111-4111-8111-111111111111',
  version: 4,
  uploadBps: 65536,
  downloadBps: 131072,
  multiplier: '1.5',
  scope: 'local',
  supported: true,
  usage: {
    up: '9007199254740993',
    down: '2',
    billed: '9007199254740995',
    quota: '9007199254741995',
    remaining: '1000',
    remainder: 501,
    unlimited: false,
  },
};
let policy = structuredClone(initialPolicy);
let submitted: unknown[];
let postResult: Msg | undefined;

beforeEach(() => {
  policy = structuredClone(initialPolicy);
  submitted = [];
  postResult = undefined;
  vi.mocked(HttpUtil.get).mockImplementation(
    async (url) =>
      new Msg(true, '', url.includes('/clients/policy/') ? structuredClone(policy) : {}),
  );
  vi.mocked(HttpUtil.post).mockImplementation(async (url, body) => {
    if (!url.includes('/clients/policy/')) return new Msg(true, '', {});
    submitted.push({ url, body });
    if (postResult) return postResult;
    policy = { ...policy, ...Object(body), version: policy.version + 1 };
    return new Msg(true, '', structuredClone(policy));
  });
});

async function openEditor() {
  const queryClient = makeTestQueryClient();
  const metadataSave = vi.fn().mockResolvedValue(new Msg(true));
  renderWithProviders(
    <ClientFormModal
      open
      mode="edit"
      client={{ email: 'alice+policy@example.com', enable: true }}
      inbounds={[]}
      save={metadataSave}
      onOpenChange={() => {}}
    />,
    { queryClient },
  );
  fireEvent.click(screen.getByRole('tab', { name: 'Traffic policy' }));
  await screen.findByLabelText('Upload limit (B/s)');
  return { queryClient, metadataSave };
}

describe('client traffic policy editor', () => {
  it('shows exact raw and fractional billed bytes beyond Number precision', async () => {
    await openEditor();
    expect(screen.getByText('9007199254740993 B')).toBeTruthy();
    expect(screen.getByText('9007199254740995.501 B')).toBeTruthy();
    expect(screen.getByText('999.499 B')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Save' })).toBeNull();
  });

  it('submits independent policy edits with the loaded identity and revision', async () => {
    const { metadataSave } = await openEditor();
    fireEvent.change(screen.getByLabelText('Upload limit (B/s)'), { target: { value: '0' } });
    fireEvent.change(screen.getByLabelText('Billing multiplier'), { target: { value: '0.001' } });
    fireEvent.click(screen.getByRole('button', { name: 'Apply traffic policy' }));
    await screen.findByText('Traffic policy applied.');
    expect(submitted).toEqual([
      {
        url: '/panel/api/clients/policy/alice%2Bpolicy%40example.com',
        body: {
          policyId: initialPolicy.policyId,
          version: 4,
          uploadBps: 0,
          downloadBps: 131072,
          multiplier: '0.001',
          scope: 'local',
        },
      },
    ]);
    expect(metadataSave).not.toHaveBeenCalled();
  });

  it('rejects fractional byte rates and a zero multiplier before sending', async () => {
    await openEditor();
    fireEvent.change(screen.getByLabelText('Upload limit (B/s)'), { target: { value: '1.5' } });
    fireEvent.change(screen.getByLabelText('Billing multiplier'), { target: { value: '0' } });
    fireEvent.click(screen.getByRole('button', { name: 'Apply traffic policy' }));
    await screen.findByText('Enter a whole number from 0 to 1099511627776 B/s.');
    await screen.findByText('Enter a multiplier from 0.001 to 1000 with at most 3 decimal places.');
    expect(submitted).toEqual([]);
  });

  it('keeps dirty values through background updates and requires explicit reload for a new revision', async () => {
    const { queryClient } = await openEditor();
    fireEvent.change(screen.getByLabelText('Upload limit (B/s)'), { target: { value: '32768' } });
    policy.version = 5;
    policy.uploadBps = 8192;
    await act(async () => {
      await queryClient.invalidateQueries({ queryKey: keys.clients.root() });
    });
    expect((screen.getByLabelText('Upload limit (B/s)') as HTMLInputElement).value).toBe('32768');
    await waitFor(() =>
      expect(
        (screen.getByRole('button', { name: 'Apply traffic policy' }) as HTMLButtonElement)
          .disabled,
      ).toBe(true),
    );
    fireEvent.click(screen.getByRole('button', { name: 'Reload saved policy' }));
    await waitFor(() =>
      expect((screen.getByLabelText('Upload limit (B/s)') as HTMLInputElement).value).toBe('8192'),
    );
    fireEvent.change(screen.getByLabelText('Upload limit (B/s)'), { target: { value: '16384' } });
    fireEvent.click(screen.getByRole('button', { name: 'Apply traffic policy' }));
    await screen.findByText('Traffic policy applied.');
    expect(submitted).toEqual([
      expect.objectContaining({ body: expect.objectContaining({ version: 5, uploadBps: 16384 }) }),
    ]);
  });

  it('retains input after an ambiguous save failure and prevents an unsafe retry', async () => {
    postResult = new Msg(false, 'policy saved; runtime update failed: unavailable');
    await openEditor();
    fireEvent.change(screen.getByLabelText('Billing multiplier'), { target: { value: '2' } });
    fireEvent.click(screen.getByRole('button', { name: 'Apply traffic policy' }));
    await screen.findByText('policy saved; runtime update failed: unavailable');
    expect((screen.getByLabelText('Billing multiplier') as HTMLInputElement).value).toBe('2');
    expect(
      (screen.getByRole('button', { name: 'Apply traffic policy' }) as HTMLButtonElement).disabled,
    ).toBe(true);
    expect(submitted).toHaveLength(1);
  });

  it('disables unsupported attachments and displays unlimited quota without fractional subtraction', async () => {
    policy.supported = false;
    policy.usage.unlimited = true;
    policy.usage.quota = '0';
    policy.usage.remaining = '0';
    await openEditor();
    expect((screen.getByLabelText('Upload limit (B/s)') as HTMLInputElement).disabled).toBe(true);
    expect(
      (screen.getByRole('button', { name: 'Apply traffic policy' }) as HTMLButtonElement).disabled,
    ).toBe(true);
    expect(screen.getAllByText('Unlimited')).toHaveLength(2);
    expect(screen.getByText(/Every attachment must support this policy/)).toBeTruthy();
    expect(screen.queryByText('-0.501 B')).toBeNull();
    expect(submitted).toEqual([]);
  });
});

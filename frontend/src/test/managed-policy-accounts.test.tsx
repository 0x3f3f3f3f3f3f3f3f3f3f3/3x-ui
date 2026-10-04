import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';

import ManagedPolicyAccounts from '@/components/clients/ManagedPolicyAccounts';
import { sections } from '@/pages/api-docs/endpoints';
import { HttpUtil, Msg } from '@/utils';

const parentClientId = '11111111-1111-4111-8111-111111111111';
afterEach(() => vi.restoreAllMocks());

function showAccounts() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <ManagedPolicyAccounts parentClientId={parentClientId} />
    </QueryClientProvider>,
  );
}

describe('managed policy account product surface', () => {
  it('queries the owned bounded API and displays exact fractions and held balance', async () => {
    const post = vi.spyOn(HttpUtil, 'post').mockResolvedValue(
      new Msg(true, '', {
        scope: 'global',
        pendingEnrollment: false,
        nextNode: '',
        accounts: [
          {
            clientId: parentClientId,
            scope: 'global',
            nodeId: '',
            policyVersion: '9007199254740993',
            enrolled: true,
            policyPending: false,
            deleted: false,
            quotaUnlimited: false,
            quotaBytes: '128',
            usage: { upload: '1', download: '0', billed: '1.5', uncertain: '0' },
            windowUsed: '1.5',
            remaining: '126.5',
            budget: { allocated: '62.5', frozen: '0', unallocated: '64' },
          },
        ],
      }),
    );
    showAccounts();
    expect((await screen.findAllByText('Shared across enrolled nodes')).length).toBeGreaterThan(0);
    for (const value of ['1.5 B', '62.5 B', '126.5 B', '64 B', '9007199254740993'])
      expect(screen.getAllByText(value).length).toBeGreaterThan(0);
    expect(post).toHaveBeenCalledWith(
      '/panel/api/server/clientPolicyCoordinator/accounts',
      { parentClientId, afterNode: '', limit: 16 },
      { silent: true },
    );
  });

  it('displays original node provenance and exact raw and billed contributions', async () => {
    vi.spyOn(HttpUtil, 'post').mockImplementation(
      async (url) =>
        new Msg(
          true,
          '',
          url.endsWith('/contributions')
            ? {
                nextGrant: '',
                contributions: [
                  {
                    nodeId: 'receipt-node-a',
                    sourceId: 'receipt-source-a',
                    bootId: 'receipt-boot-a',
                    grantId: 'receipt-authority:17',
                    grantSequence: '17',
                    reportSequence: '3',
                    sealed: true,
                    usage: { upload: '4', download: '3', billed: '10.5', uncertain: '0' },
                  },
                ],
              }
            : {
                scope: 'global',
                pendingEnrollment: false,
                nextNode: '',
                accounts: [
                  {
                    clientId: parentClientId,
                    scope: 'global',
                    nodeId: '',
                    policyVersion: '7',
                    enrolled: true,
                    policyPending: false,
                    deleted: false,
                    quotaUnlimited: false,
                    quotaBytes: '8192',
                    usage: { upload: '7', download: '6', billed: '19.5', uncertain: '0' },
                    windowUsed: '19.5',
                    remaining: '8172.5',
                    budget: { allocated: '0', frozen: '0', unallocated: '8172.5' },
                  },
                ],
              },
        ),
    );
    showAccounts();
    expect(await screen.findByText('7 B')).toBeTruthy();
    expect(screen.getByText('6 B')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Node usage receipts' }));
    expect(await screen.findByText('receipt-node-a')).toBeTruthy();
    for (const value of [
      '4 B',
      '3 B',
      '10.5 B',
      'receipt-source-a',
      'receipt-boot-a',
      'receipt-authority:17',
    ])
      expect(screen.getAllByText(value).length).toBeGreaterThan(0);
  });

  it('shows pending enrollment without inventing a balance', async () => {
    vi.spyOn(HttpUtil, 'post').mockResolvedValue(
      new Msg(true, '', { scope: 'node', pendingEnrollment: true, nextNode: '', accounts: [] }),
    );
    showAccounts();
    expect(await screen.findByText('Awaiting node enrollment')).toBeTruthy();
    expect(screen.queryByText('128 B')).toBeNull();
  });

  it('keeps unavailable account state visible when its API refuses admission', async () => {
    vi.spyOn(HttpUtil, 'post').mockResolvedValue(new Msg(false, 'private diagnostics', null));
    showAccounts();
    await waitFor(() =>
      expect(screen.getByRole('alert').textContent).toContain('Account status unavailable'),
    );
    expect(screen.queryByText('private diagnostics')).toBeNull();
  });

  it('documents generated status and account schemas on the protected API paths', () => {
    const endpoints = sections.flatMap((section) => section.endpoints);
    for (const [method, suffix, schema] of [
      ['GET', '', 'ManagedPolicyCoordinatorStatus'],
      ['POST', '/activate', 'ManagedPolicyCoordinatorStatus'],
      ['POST', '/accounts', 'ManagedPolicyAccountPage'],
      ['POST', '/contributions', 'ManagedPolicyContributionPage'],
      ['POST', '/enroll', 'ManagedPolicyEnrollmentResult'],
    ]) {
      const endpoint = endpoints.find(
        (entry) =>
          entry.method === method &&
          entry.path === '/panel/api/server/clientPolicyCoordinator' + suffix,
      );
      expect(endpoint?.responseSchema).toBe(schema);
    }
  });
});

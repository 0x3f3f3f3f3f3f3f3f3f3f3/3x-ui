import { render, screen, waitFor } from '@testing-library/react';
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
      expect(screen.getByText(value)).toBeTruthy();
    expect(post).toHaveBeenCalledWith(
      '/panel/api/server/clientPolicyCoordinator/accounts',
      { parentClientId, afterNode: '', limit: 16 },
      { silent: true },
    );
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

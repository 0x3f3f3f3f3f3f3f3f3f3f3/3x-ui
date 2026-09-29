import type { ClientPolicyAccounting } from '@/schemas/client';

export const pendingAccounting: ClientPolicyAccounting = {
  clientId: 'e18c9a96-71bf-48d4-933f-8b9a46d4290c',
  lifetime: { upload: '25', download: '0', billed: '50', uncertain: '0' },
  period: { upload: '10', download: '0', billed: '20', uncertain: '0' },
  quotaBytes: '100',
  remaining: '80',
  appliedVersion: '2',
  desiredVersion: '3',
  resetPending: true,
};

export const acknowledgedAccounting: ClientPolicyAccounting = {
  ...pendingAccounting,
  period: { upload: '0', download: '0', billed: '0', uncertain: '0' },
  remaining: '100',
  appliedVersion: '3',
  resetPending: false,
};

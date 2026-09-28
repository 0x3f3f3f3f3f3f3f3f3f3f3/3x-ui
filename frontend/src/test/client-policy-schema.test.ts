import { expect, it } from 'vitest';
import { ClientPolicySchema, ClientPolicyUpdateSchema } from '@/schemas/client-policy';

const update = {
  policyId: '11111111-1111-4111-8111-111111111111',
  version: 0,
  uploadBps: 0,
  downloadBps: 1099511627776,
  multiplier: '0.001',
  scope: 'local',
};

it('accepts exact policy boundaries and rejects rates or multipliers the backend cannot execute', () => {
  expect(ClientPolicyUpdateSchema.parse(update)).toEqual(update);
  expect(ClientPolicyUpdateSchema.parse({ ...update, multiplier: '1000.000' }).multiplier).toBe(
    '1000.000',
  );
  for (const uploadBps of [-1, 0.5, 1099511627777, NaN, Infinity, null, '10']) {
    expect(
      ClientPolicyUpdateSchema.safeParse({ ...update, uploadBps }).success,
      String(uploadBps),
    ).toBe(false);
  }
  for (const multiplier of [
    '0',
    '-1',
    '0.0001',
    '1000.001',
    '01',
    '.5',
    '1.',
    '1e2',
    ' 1',
    'NaN',
  ]) {
    expect(ClientPolicyUpdateSchema.safeParse({ ...update, multiplier }).success, multiplier).toBe(
      false,
    );
  }
});

it('rejects malformed or overflowing usage as validation errors instead of throwing in BigInt conversion', () => {
  for (const up of ['garbage', '1.2', '1e10', '9223372036854775808', 9007199254740992]) {
    const result = ClientPolicySchema.safeParse({
      ...update,
      supported: true,
      usage: {
        up,
        down: '0',
        billed: '0',
        quota: '0',
        remaining: '0',
        remainder: 0,
        unlimited: true,
      },
    });
    expect(result.success, String(up)).toBe(false);
    if (!result.success) expect(result.error.issues[0].path).toEqual(['usage', 'up']);
  }
});

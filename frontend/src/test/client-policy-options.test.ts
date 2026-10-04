import { describe, expect, it } from 'vitest';

import { ClientPolicyOptionsSchema } from '@/schemas/client';

describe('client policy wire validation', () => {
  it('preserves optional scope and rejects unknown scope', () => {
    const legacy = { uploadBytesPerSecond: 0, downloadBytesPerSecond: 0, multiplier: '2' };
    expect(ClientPolicyOptionsSchema.parse(legacy)).toEqual(legacy);
    for (const scope of ['node', 'global']) {
      expect(ClientPolicyOptionsSchema.parse({ ...legacy, scope })).toEqual({ ...legacy, scope });
    }
    for (const scope of ['', 'GLOBAL', 'global ', 'all']) {
      expect(ClientPolicyOptionsSchema.safeParse({ ...legacy, scope }).success).toBe(false);
    }
  });
  it.each(['0', '-1', '1000.000001', '0.1234567', '1e2', ' 1', '1.'])(
    'rejects unsupported multiplier %s',
    (multiplier) => {
      expect(
        ClientPolicyOptionsSchema.safeParse({
          uploadBytesPerSecond: 0,
          downloadBytesPerSecond: 0,
          multiplier,
        }).success,
      ).toBe(false);
    },
  );

  it.each([-1, 1.5, 1099511627777])('rejects invalid byte rate %s in either direction', (rate) => {
    for (const direction of ['uploadBytesPerSecond', 'downloadBytesPerSecond']) {
      expect(
        ClientPolicyOptionsSchema.safeParse({
          uploadBytesPerSecond: 0,
          downloadBytesPerSecond: 0,
          multiplier: '1',
          [direction]: rate,
        }).success,
      ).toBe(false);
    }
  });

  it.each(['', '0.000001', '1000', '0001.234567'])(
    'preserves supported exact multiplier %s',
    (multiplier) => {
      const policy = {
        uploadBytesPerSecond: 1099511627776,
        downloadBytesPerSecond: 0,
        multiplier,
      };
      expect(ClientPolicyOptionsSchema.parse(policy)).toEqual(policy);
    },
  );
});

import { describe, expect, it } from 'vitest';

import { formValuesToWirePayload, rawInboundToFormValues } from '@/lib/xray/inbound-form-adapter';
import { InboundFormSchema } from '@/schemas/forms/inbound-form';

const ownerClientId = 'ba640288-39f8-48bd-93c0-7eccd011f6ca';

describe('password proxy account ownership form', () => {
  it.each(['mixed', 'http'] as const)(
    'preserves %s account ownership through validated edits',
    (protocol) => {
      const account = { user: '用户', pass: 'resource-password', ownerClientId };
      const values = rawInboundToFormValues({
        protocol,
        port: 1080,
        listen: '127.0.0.1',
        settings:
          protocol === 'mixed'
            ? { auth: 'password', accounts: [account], udp: true, ip: '127.0.0.1' }
            : { accounts: [account], allowTransparent: false, requireAuthentication: true },
      });
      const parsed = InboundFormSchema.parse(values);
      const payload = formValuesToWirePayload(parsed);
      const settings = JSON.parse(payload.settings);
      expect(settings.accounts).toEqual([account]);
      if (protocol === 'http') expect(settings.requireAuthentication).toBe(true);
    },
  );

  it('preserves protected empty HTTP authentication', () => {
    const parsed = InboundFormSchema.parse(
      rawInboundToFormValues({
        protocol: 'http',
        port: 1080,
        settings: { accounts: [], requireAuthentication: true },
      }),
    );
    expect(JSON.parse(formValuesToWirePayload(parsed).settings).requireAuthentication).toBe(true);
  });

  it.each(['mixed', 'http'] as const)('rejects partially owned %s account lists', (protocol) => {
    const result = InboundFormSchema.safeParse(
      rawInboundToFormValues({
        protocol,
        port: 1080,
        settings: {
          auth: 'password',
          accounts: [
            { user: 'owned', pass: 'owned-password', ownerClientId },
            { user: 'unowned', pass: 'unowned-password' },
          ],
        },
      }),
    );
    expect(result.success).toBe(false);
    if (!result.success) {
      expect(result.error.issues).toEqual(
        expect.arrayContaining([
          expect.objectContaining({
            path: ['settings', 'accounts', 1, 'ownerClientId'],
            message: 'pages.inbounds.form.ownerClientRequired',
          }),
        ]),
      );
    }
  });

  it('rejects owned Mixed noauth without discarding stored credentials', () => {
    const values = rawInboundToFormValues({
      protocol: 'mixed',
      port: 1080,
      settings: { auth: 'noauth', accounts: [{ user: 'owned', pass: 'secret', ownerClientId }] },
    });
    const result = InboundFormSchema.safeParse(values);
    expect(result.success).toBe(false);
    if (!result.success) {
      expect(result.error.issues).toEqual(
        expect.arrayContaining([
          expect.objectContaining({
            path: ['settings', 'auth'],
            message: 'pages.inbounds.form.passwordOwnerRequiresAuth',
          }),
        ]),
      );
    }
    if (values.protocol !== 'mixed') throw new Error('fixture did not retain Mixed protocol');
    expect(values.settings.accounts).toEqual([{ user: 'owned', pass: 'secret', ownerClientId }]);
  });
});

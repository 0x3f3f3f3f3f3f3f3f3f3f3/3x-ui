import { z } from 'zod';

import {
  PasswordProxyAccountSchema,
  validatePasswordAccountOwners,
} from './password-proxy-account';

export const MixedAuthSchema = z.enum(['password', 'noauth']);
export type MixedAuth = z.infer<typeof MixedAuthSchema>;

// SOCKS/HTTP combined inbound. When auth==='noauth' the `accounts` field is
// omitted from the wire payload (the panel writes `undefined`), so we accept
// either an array or absence here.
export const MixedAccountSchema = PasswordProxyAccountSchema;
export type MixedAccount = z.infer<typeof MixedAccountSchema>;

export const MixedInboundSettingsSchema = z
  .object({
    auth: MixedAuthSchema.default('password'),
    accounts: z.array(MixedAccountSchema).optional(),
    udp: z.boolean().default(false),
    ip: z.string().default('127.0.0.1'),
  })
  .superRefine((settings, ctx) => {
    validatePasswordAccountOwners(settings.accounts, ctx);
    if (
      settings.auth !== 'password' &&
      settings.accounts?.some((account) => account.ownerClientId)
    ) {
      ctx.addIssue({
        code: 'custom',
        path: ['auth'],
        message: 'pages.inbounds.form.passwordOwnerRequiresAuth',
      });
    }
  });
export type MixedInboundSettings = z.infer<typeof MixedInboundSettingsSchema>;

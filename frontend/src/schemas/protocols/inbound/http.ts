import { z } from 'zod';

import {
  PasswordProxyAccountSchema,
  validatePasswordAccountOwners,
} from './password-proxy-account';

// HTTP proxy inbound — a classic forward proxy. Accounts are user/pass pairs;
// `allowTransparent` exposes Xray's option to forward requests with the
// original Host header. ownerClientId selects a canonical panel owner; runtime
// identity is generated from validated database memberships.
export const HttpAccountSchema = PasswordProxyAccountSchema;
export type HttpAccount = z.infer<typeof HttpAccountSchema>;

export const HttpInboundSettingsSchema = z
  .object({
    accounts: z.array(HttpAccountSchema).default([]),
    allowTransparent: z.boolean().default(false),
    requireAuthentication: z.boolean().optional(),
  })
  .superRefine((settings, ctx) => validatePasswordAccountOwners(settings.accounts, ctx));
export type HttpInboundSettings = z.infer<typeof HttpInboundSettingsSchema>;

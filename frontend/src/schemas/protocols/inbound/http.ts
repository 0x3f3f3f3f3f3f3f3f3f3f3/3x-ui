import { z } from 'zod';

// HTTP proxy inbound — a classic forward proxy. Accounts are user/pass pairs;
// `allowTransparent` exposes Xray's option to forward requests with the
// original Host header. ownerClientId selects a canonical panel owner; runtime
// identity is generated from validated database memberships.
export const HttpAccountSchema = z.object({
  user: z.string().min(1),
  pass: z.string().min(1),
  ownerClientId: z.uuid().optional(),
});
export type HttpAccount = z.infer<typeof HttpAccountSchema>;

export const HttpInboundSettingsSchema = z.object({
  accounts: z.array(HttpAccountSchema).default([]),
  allowTransparent: z.boolean().default(false),
  requireAuthentication: z.boolean().optional(),
});
export type HttpInboundSettings = z.infer<typeof HttpInboundSettingsSchema>;

import { z } from 'zod';

import { MieruCredentialSchema, MieruMtuSchema, MieruTransportSchema } from '../shared/mieru';

// Panel clients retain their shared SQL metadata. Runtime projects native users.
export const MieruClientSchema = z
  .object({
    email: z.string().min(1),
    mieruUsername: MieruCredentialSchema.default(''),
    mieruPassword: MieruCredentialSchema.default(''),
    enable: z.boolean().default(true),
  })
  .loose();
export type MieruClient = z.infer<typeof MieruClientSchema>;

export const MieruInboundSettingsSchema = z
  .object({
    clients: z.array(MieruClientSchema).default([]),
    transport: MieruTransportSchema.default('TCP'),
    mtu: MieruMtuSchema.default(1400),
    userHintRequired: z.boolean().default(false),
    maxConnections: z.number().int().min(0).max(0xffffffff).default(1024),
    handshakeTimeoutSeconds: z.number().int().min(0).max(0xffffffff).default(10),
  })
  .strict();
export type MieruInboundSettings = z.infer<typeof MieruInboundSettingsSchema>;

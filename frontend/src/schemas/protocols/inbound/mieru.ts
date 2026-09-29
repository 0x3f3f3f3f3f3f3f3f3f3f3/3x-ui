import { z } from 'zod';
import { ManagedClientSchema } from './managed-client';

export const MieruCredentialsSchema = z.object({
  email: z
    .string()
    .refine(
      (value) =>
        value.length > 0 && value.trim() === value && new TextEncoder().encode(value).length <= 64,
      'pages.clients.mieruUsernameInvalid',
    ),
  password: z
    .string()
    .refine(
      (value) => value.length > 0 && new TextEncoder().encode(value).length <= 64,
      'pages.clients.mieruPasswordInvalid',
    ),
});

export const MieruManagedClientSchema = ManagedClientSchema.extend(MieruCredentialsSchema.shape);

export const MieruInboundSettingsSchema = z.object({
  network: z.enum(['tcp', 'udp', 'both']).default('tcp'),
  bridgePort: z.number().int().min(1).max(65535).optional(),
  clients: z
    .array(MieruManagedClientSchema)
    .nullish()
    .transform((clients) => clients ?? []),
});
export type MieruInboundSettings = z.infer<typeof MieruInboundSettingsSchema>;

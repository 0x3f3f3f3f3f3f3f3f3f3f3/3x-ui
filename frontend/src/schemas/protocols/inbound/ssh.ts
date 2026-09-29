import { z } from 'zod';
import { SSHClientSchema } from '@/schemas/ssh';
import { ManagedClientSchema } from './managed-client';

export const SSHManagedClientSchema = ManagedClientSchema.extend({ ssh: SSHClientSchema });

export const SSHInboundSettingsSchema = z.object({
  hostKey: z.string().optional(),
  bridgePort: z.number().int().min(1).max(65535).optional(),
  clients: z
    .array(SSHManagedClientSchema)
    .nullish()
    .transform((clients) => clients ?? []),
});
export type SSHInboundSettings = z.infer<typeof SSHInboundSettingsSchema>;

import { z } from 'zod';
import { SSHClientSchema } from '@/schemas/ssh';

export const SSHManagedClientSchema = z.object({
  email: z.string().min(1).max(64),
  ssh: SSHClientSchema,
  enable: z.boolean().default(true),
  totalGB: z.number().int().min(0).default(0),
  expiryTime: z.number().int().default(0),
  subId: z.string().default(''),
  group: z.string().optional(),
  comment: z.string().default(''),
  limitIp: z.number().int().min(0).default(0),
  tgId: z
    .union([z.number(), z.string()])
    .transform((v) => Number(v) || 0)
    .default(0),
  reset: z.number().int().min(0).default(0),
  resetDay: z.number().int().min(0).max(31).default(0),
  resetWeekday: z.number().int().min(0).max(7).default(0),
  resetMax: z.number().int().min(0).default(0),
  trafficReset: z.enum(['never', 'hourly', 'daily', 'weekly', 'monthly']).optional(),
  trafficResetDay: z.number().int().min(1).max(31).optional(),
  created_at: z.number().int().optional(),
  updated_at: z.number().int().optional(),
});

export const SSHInboundSettingsSchema = z.object({
  hostKey: z.string().optional(),
  bridgePort: z.number().int().min(1).max(65535).optional(),
  clients: z
    .array(SSHManagedClientSchema)
    .nullish()
    .transform((clients) => clients ?? []),
});
export type SSHInboundSettings = z.infer<typeof SSHInboundSettingsSchema>;

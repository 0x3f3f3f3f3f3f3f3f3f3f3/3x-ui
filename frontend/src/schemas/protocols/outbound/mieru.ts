import { z } from 'zod';

import { PortSchema } from '@/schemas/primitives';
import {
  MieruCredentialSchema,
  MieruMtuSchema,
  MieruMultiplexingSchema,
  MieruTransportSchema,
} from '../shared/mieru';

export const MieruOutboundSettingsSchema = z
  .object({
    address: z.string().min(1),
    port: PortSchema,
    username: MieruCredentialSchema.min(1),
    password: MieruCredentialSchema.min(1),
    transport: MieruTransportSchema.default('TCP'),
    mtu: MieruMtuSchema.default(1400),
    multiplexing: MieruMultiplexingSchema.default('MULTIPLEXING_LOW'),
  })
  .strict();
export type MieruOutboundSettings = z.infer<typeof MieruOutboundSettingsSchema>;

import { z } from 'zod';
import {
  SnellOptionFields,
  SnellPSKSchema,
  snellPSKBytes,
  validateSnellOptions,
} from '../shared/snell';

export const SnellClientSchema = z
  .object({
    email: z.string().min(1),
    snellPsk: SnellPSKSchema.default(''),
    enable: z.boolean().default(true),
  })
  .loose()
  .superRefine((value, ctx) => {
    if ('clientId' in value || 'client_id' in value)
      ctx.addIssue({ code: 'custom', message: 'pages.inbounds.form.snell.ownerRequired' });
  });
export const SnellInboundSettingsSchema = z
  .object({
    ...SnellOptionFields,
    clients: z
      .array(SnellClientSchema)
      .max(1, 'pages.inbounds.form.snell.exclusiveOwner')
      .default([]),
  })
  .strict()
  .superRefine((value, ctx) => {
    validateSnellOptions(value, ctx);
    for (const client of value.clients) {
      if (value.version === 6 && client.snellPsk !== '' && snellPSKBytes(client.snellPsk) < 12)
        ctx.addIssue({
          code: 'custom',
          path: ['clients'],
          message: 'pages.clients.snellPskV6Required',
        });
    }
  });
export type SnellInboundSettings = z.infer<typeof SnellInboundSettingsSchema>;

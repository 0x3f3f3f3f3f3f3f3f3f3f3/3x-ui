import { z } from 'zod';
import { PortSchema } from '@/schemas/primitives';
import {
  SnellOptionFields,
  SnellPSKSchema,
  snellPSKBytes,
  validateSnellOptions,
} from '../shared/snell';

export const SnellOutboundSettingsSchema = z
  .object({
    ...SnellOptionFields,
    address: z
      .string()
      .min(1)
      .refine(
        (value) =>
          value.trim() === value &&
          !/[\s/,?#@"']/.test(value) &&
          (!value.includes(':') || z.ipv6().safeParse(value).success),
        'pages.inbounds.form.snell.addressInvalid',
      ),
    port: PortSchema,
    psk: SnellPSKSchema.refine((value) => value.length > 0, 'pages.clients.snellPskInvalid'),
    obfsHost: z.string().default(''),
    obfsUri: z.string().default(''),
    reuse: z.boolean().default(true),
  })
  .strict()
  .superRefine((value, ctx) => {
    validateSnellOptions(value, ctx);
    if (value.version === 6 && snellPSKBytes(value.psk) < 12)
      ctx.addIssue({ code: 'custom', path: ['psk'], message: 'pages.clients.snellPskV6Required' });
    if (value.obfs !== 'http' && (value.obfsHost !== '' || value.obfsUri !== ''))
      ctx.addIssue({ code: 'custom', message: 'pages.inbounds.form.snell.optionsInvalid' });
  });
export type SnellOutboundSettings = z.infer<typeof SnellOutboundSettingsSchema>;

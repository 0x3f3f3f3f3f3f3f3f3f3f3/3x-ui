import { z } from 'zod';

import {
  ClientPolicySchema as GeneratedPolicySchema,
  ClientPolicyUpdateSchema as GeneratedUpdateSchema,
  ClientPolicyUsageSchema as GeneratedUsageSchema,
  ClientBillingSchema as GeneratedBillingSchema,
} from '@/generated/zod';

const RateSchema = z
  .number({ error: 'pages.clients.policy.invalidRate' })
  .int('pages.clients.policy.invalidRate')
  .min(0, 'pages.clients.policy.invalidRate')
  .max(1099511627776, 'pages.clients.policy.invalidRate');
const MultiplierSchema = z.string().refine((value) => {
  if (!/^(0|[1-9]\d{0,3})(\.\d{1,3})?$/.test(value)) return false;
  const [whole, fraction = ''] = value.split('.');
  const milli = BigInt(whole + fraction.padEnd(3, '0'));
  return milli > 0n && milli <= 1000000n;
}, 'pages.clients.policy.invalidMultiplier');
const BytesSchema = z
  .string()
  .regex(/^(0|[1-9]\d{0,18})$/)
  .pipe(z.string().refine((value) => BigInt(value) <= 9223372036854775807n));

export const ClientPolicyUpdateSchema = GeneratedUpdateSchema.extend({
  policyId: z.string().min(1),
  version: z.number().int().nonnegative().max(Number.MAX_SAFE_INTEGER),
  uploadBps: RateSchema,
  downloadBps: RateSchema,
  multiplier: MultiplierSchema,
  scope: z.literal('local'),
});

export const ClientPolicySchema = GeneratedPolicySchema.extend({
  ...ClientPolicyUpdateSchema.shape,
  scope: z.string(),
  usage: GeneratedUsageSchema.extend({
    up: BytesSchema,
    down: BytesSchema,
    billed: BytesSchema,
    quota: BytesSchema,
    remaining: BytesSchema,
    remainder: z.number().int().min(0).max(999),
  }),
});

export type ClientPolicy = z.infer<typeof ClientPolicySchema>;
export type ClientPolicyUpdate = z.infer<typeof ClientPolicyUpdateSchema>;

export const ClientBillingSchema = GeneratedBillingSchema.extend({
  ...ClientPolicySchema.shape.usage.shape,
  multiplier: MultiplierSchema,
});
export type ClientBilling = z.infer<typeof ClientBillingSchema>;

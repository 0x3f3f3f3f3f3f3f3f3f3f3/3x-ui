import { z } from 'zod';

import { PortSchema } from '@/schemas/primitives';

export const TunnelNetworkSchema = z.enum(['tcp', 'udp', 'tcp,udp']);
export type TunnelNetwork = z.infer<typeof TunnelNetworkSchema>;

const sourceCIDR = z.union([
  z.cidrv4(),
  z.cidrv6().refine((source) => {
    try {
      const address = new URL(`http://[${source.split('/')[0]}]`).hostname;
      return !/^\[::ffff:[\da-f]+:[\da-f]+\]$/i.test(address);
    } catch {
      return false;
    }
  }),
]);

// Tunnel inbound (Xray's `dokodemo-door`-style transparent forwarder).
// `portMap` is persisted as Record<string, string> on the wire — the panel
// flattens an internal array-of-{name,value} into that map via toV2Headers
// with arr=false.
export const TunnelInboundSettingsSchema = z.object({
  clients: z.array(z.record(z.string(), z.unknown())).nullable().optional(),
  outboundTag: z.string().nullable().optional(),
  allowedSourceCidrs: z
    .array(z.string())
    .max(256, { error: 'pages.inbounds.form.allowedSourceCidrsInvalid' })
    .refine((sources) => sources.every((source) => sourceCIDR.safeParse(source).success), {
      error: 'pages.inbounds.form.allowedSourceCidrsInvalid',
    })
    .nullable()
    .optional(),
  rewriteAddress: z.string().optional(),
  // AntD InputNumber writes null when cleared; accept it and collapse to
  // undefined so the field is omitted from the payload instead of crashing
  // validation with "Invalid input" (issue #5516). The trailing .optional()
  // keeps the key optional in the inferred type (a bare .transform() would
  // make it required).
  rewritePort: PortSchema.nullable()
    .transform((v) => v ?? undefined)
    .optional(),
  portMap: z.record(z.string(), z.string()).default({}),
  allowedNetwork: TunnelNetworkSchema.default('tcp,udp'),
  followRedirect: z.boolean().default(false),
});
export type TunnelInboundSettings = z.infer<typeof TunnelInboundSettingsSchema>;

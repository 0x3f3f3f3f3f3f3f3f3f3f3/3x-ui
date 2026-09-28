import { z } from 'zod';

import { PortSchema } from '@/schemas/primitives';
import { SSHPublicKeySchema } from '@/schemas/ssh';

const byteLength = (value: string) => new TextEncoder().encode(value).length;
const control = /\p{Cc}/u;
const ip = z.union([z.ipv4(), z.ipv6()]);
const hostname =
  /^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$/;

export const SSHOutboundTagSchema = z
  .string()
  .refine(
    (value) =>
      value.length > 0 &&
      byteLength(value) <= 128 &&
      value.trim() === value &&
      !control.test(value),
    'pages.xray.sshOutbound.invalidTag',
  );

export const SSHOutboundSettingsSchema = z.strictObject({
  address: z
    .string()
    .trim()
    .refine((value) => {
      const host = value.startsWith('[') && value.endsWith(']') ? value.slice(1, -1) : value;
      return ip.safeParse(host).success || (host.length <= 253 && hostname.test(host));
    }, 'pages.xray.sshOutbound.invalidAddress'),
  port: PortSchema,
  user: z
    .string()
    .refine(
      (value) =>
        value.length > 0 &&
        byteLength(value) <= 255 &&
        value.trim() === value &&
        !control.test(value),
      'pages.xray.sshOutbound.invalidAccount',
    ),
  privateKey: z
    .string()
    .refine(
      (value) => value.trim().length > 0 && byteLength(value) <= 65536,
      'pages.xray.sshOutbound.invalidPrivateKey',
    ),
  privateKeyPassphrase: z
    .string()
    .refine((value) => byteLength(value) <= 4096, 'pages.xray.sshOutbound.invalidPassphrase')
    .optional(),
  hostKey: SSHPublicKeySchema.refine(
    (value) => byteLength(value) <= 16384,
    'pages.clients.ssh.invalidPublicKey',
  ),
});
export type SSHOutboundSettings = z.infer<typeof SSHOutboundSettingsSchema>;

export const SSHOutboundSchema = z.strictObject({
  protocol: z.literal('ssh'),
  tag: SSHOutboundTagSchema,
  settings: SSHOutboundSettingsSchema,
});

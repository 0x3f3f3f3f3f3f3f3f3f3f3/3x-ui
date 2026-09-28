import { z } from 'zod';
import {
  SSHClientSchema as GeneratedSSHClientSchema,
  SSHRemoteBindSchema as GeneratedSSHRemoteBindSchema,
  SSHTargetSchema as GeneratedSSHTargetSchema,
} from '@/generated/zod';

function isPublicKey(value: string): boolean {
  if (/[\r\n]/.test(value)) return false;
  const [kind, encoded] = value.split(/[ \t]+/);
  if (!kind || !encoded || kind.includes('-cert-')) return false;
  if (!/^(ssh-|ecdsa-|sk-)[A-Za-z0-9@._+-]+$/.test(kind)) return false;
  if (!/^[A-Za-z0-9+/]+={0,2}$/.test(encoded)) return false;
  try {
    const bytes = Uint8Array.from(atob(encoded), (c) => c.charCodeAt(0));
    if (bytes.length < 4) return false;
    const length = new DataView(bytes.buffer).getUint32(0);
    return (
      length === kind.length &&
      bytes.length > length + 4 &&
      new TextDecoder().decode(bytes.subarray(4, 4 + length)) === kind
    );
  } catch {
    return false;
  }
}

export const SSHPublicKeySchema = z
  .string()
  .trim()
  .max(16384)
  .refine(isPublicKey, 'pages.clients.ssh.invalidPublicKey');
const SSHPortSchema = z.number().int().min(0).max(65535);
const SSHAddressSchema = z.union([z.ipv4(), z.ipv6()]);
const SSHHostSchema = z
  .string()
  .trim()
  .max(253)
  .refine((host) => {
    if (host === '*' || SSHAddressSchema.safeParse(host).success) return true;
    return host
      .replace(/\.$/, '')
      .split('.')
      .every(
        (part) =>
          part.length > 0 && part.length <= 63 && /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/i.test(part),
      );
  }, 'pages.clients.ssh.invalidTarget');

export const SSHTargetSchema = GeneratedSSHTargetSchema.extend({
  host: SSHHostSchema,
  port: SSHPortSchema,
});
export const SSHRemoteBindSchema = GeneratedSSHRemoteBindSchema.extend({
  address: z
    .string()
    .trim()
    .refine(
      (address) => SSHAddressSchema.safeParse(address).success,
      'pages.clients.ssh.invalidReverseAddress',
    ),
  port: SSHPortSchema,
});
export const SSHClientSchema = GeneratedSSHClientSchema.extend({
  publicKeys: z
    .array(z.string())
    .transform((keys) => keys.filter((key) => key.trim() !== ''))
    .pipe(z.array(SSHPublicKeySchema).min(1, 'pages.clients.ssh.publicKeyRequired').max(16)),
  targets: z
    .array(SSHTargetSchema)
    .max(256)
    .nullish()
    .transform((targets) => targets ?? []),
  reverse: z
    .array(SSHRemoteBindSchema)
    .max(16)
    .nullish()
    .transform((reverse) => reverse ?? []),
});
export type SSHClient = z.infer<typeof SSHClientSchema>;

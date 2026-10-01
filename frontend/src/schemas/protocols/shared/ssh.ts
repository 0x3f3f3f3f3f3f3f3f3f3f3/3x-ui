import { z } from 'zod';
import { SockoptStreamSettingsSchema } from '@/schemas/protocols/stream/sockopt';

function validUtf8(value: string, limit: number): boolean {
  return (
    !Array.from(value).some((char) => {
      const code = char.codePointAt(0)!;
      return code >= 0xd800 && code <= 0xdfff;
    }) && new TextEncoder().encode(value).length <= limit
  );
}
export const SSHUsernameSchema = z
  .string()
  .refine(
    (value) => validUtf8(value, 256) && !/[\p{White_Space}\p{Cc}]/u.test(value),
    'pages.clients.sshUsernameInvalid',
  );
export const SSHPasswordSchema = z
  .string()
  .refine((value) => validUtf8(value, 1024), 'pages.clients.sshPasswordInvalid');

// The backend's SSH parser is authoritative. Here reject options, certificates,
// invalid wire type/base64 and native resource overflow before submission.
export function isSSHPublicKey(line: string): boolean {
  const fields = line.trim().split(/\s+/);
  const [type, encoded] = fields;
  if (
    !type ||
    !encoded ||
    type.includes('-cert-') ||
    !/^(ssh-|ecdsa-sha2-|sk-)/.test(type) ||
    !/^[A-Za-z0-9+/]+={0,2}$/.test(encoded)
  )
    return false;
  try {
    const raw = atob(encoded);
    if (raw.length < 8) return false;
    const size =
      ((raw.charCodeAt(0) << 24) |
        (raw.charCodeAt(1) << 16) |
        (raw.charCodeAt(2) << 8) |
        raw.charCodeAt(3)) >>>
      0;
    if (size > raw.length - 8 || raw.slice(4, 4 + size) !== type) return false;
    if (type === 'ssh-ed25519')
      return (
        raw.length === size + 40 &&
        raw.slice(size + 4, size + 8) === String.fromCharCode(0, 0, 0, 32)
      );
    return true;
  } catch {
    return false;
  }
}
export const SSHAuthorizedKeysSchema = z.string().refine((value) => {
  const lines = value.split('\n');
  return (
    lines.every((line) => validUtf8(line, 16384)) &&
    lines.filter((line) => line.trim()).length <= 16 &&
    lines.every((line) => !line.trim() || isSSHPublicKey(line))
  );
}, 'pages.clients.sshPublicKeysInvalid');
export const SSHHostKeySchema = z
  .string()
  .refine(isSSHPublicKey, 'pages.xray.outboundForm.sshHostKeyRequired');

export const SSHNativeStreamSchema = z
  .object({
    network: z.enum(['', 'tcp', 'raw']).optional(),
    security: z.enum(['', 'none']).optional(),
    sockopt: SockoptStreamSettingsSchema.optional(),
  })
  .strict();
export function isSSHNativeWireMux(mux: unknown): boolean {
  if (mux == null) return true;
  if (typeof mux !== 'object' || Array.isArray(mux)) return false;
  const fields = mux as Record<string, unknown>;
  return (
    (fields.enabled == null || fields.enabled === false) &&
    (fields.xudpConcurrency == null || fields.xudpConcurrency === 0) &&
    (fields.xudpProxyUDP443 == null || fields.xudpProxyUDP443 === '')
  );
}
export function sshNativeFormGuard(standardStreamSchema: z.ZodType) {
  return z.unknown().superRefine((input, ctx) => {
    if (!input || typeof input !== 'object') return;
    const value = input as {
      protocol?: string;
      port?: unknown;
      streamSettings?: unknown;
      mux?: { enabled?: boolean };
      nativeWireOptionsError?: string;
    };
    if (value.protocol !== 'ssh') {
      if (value.streamSettings != null) {
        const parsed = standardStreamSchema.safeParse(value.streamSettings);
        if (!parsed.success)
          for (const issue of parsed.error.issues)
            ctx.addIssue({ ...issue, path: ['streamSettings', ...issue.path] });
      }
      return;
    }
    if (
      'port' in value &&
      (typeof value.port !== 'number' ||
        !Number.isInteger(value.port) ||
        value.port < 1 ||
        value.port > 65535)
    )
      ctx.addIssue({
        code: 'custom',
        path: ['port'],
        message: 'pages.inbounds.form.ssh.portInvalid',
      });
    if (
      value.nativeWireOptionsError ||
      (value.streamSettings != null &&
        !SSHNativeStreamSchema.safeParse(value.streamSettings).success) ||
      value.mux?.enabled
    )
      ctx.addIssue({
        code: 'custom',
        path: ['streamSettings'],
        message: 'pages.inbounds.form.ssh.nativeTransportOnly',
      });
  });
}

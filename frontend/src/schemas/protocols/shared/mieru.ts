import { z } from 'zod';

export const MieruCredentialSchema = z.string().refine(
  (value) =>
    !Array.from(value).some((char) => {
      const code = char.codePointAt(0)!;
      return code >= 0xd800 && code <= 0xdfff;
    }) && new TextEncoder().encode(value).length <= 64,
  'pages.clients.mieruCredentialInvalid',
);

export const MieruTransportSchema = z.enum(['TCP', 'UDP']);
export const MieruMtuSchema = z.union([z.literal(0), z.number().int().min(1280).max(1500)]);
export const MieruMultiplexingSchema = z.enum([
  'MULTIPLEXING_OFF',
  'MULTIPLEXING_LOW',
  'MULTIPLEXING_MIDDLE',
  'MULTIPLEXING_HIGH',
]);

export function isMieruNativeStream(stream: unknown): boolean {
  if (stream == null) return true;
  const value = stream as Record<string, unknown>;
  if (value.network && value.network !== 'tcp') return false;
  if (value.security && value.security !== 'none') return false;
  const tcp = value.tcpSettings as
    | { header?: { type?: string }; acceptProxyProtocol?: boolean }
    | undefined;
  const sockopt = value.sockopt as { acceptProxyProtocol?: boolean } | undefined;
  const mask = value.finalmask as { tcp?: unknown[]; udp?: unknown[] } | undefined;
  return (
    !(tcp?.header?.type && tcp.header.type !== 'none') &&
    !tcp?.acceptProxyProtocol &&
    !sockopt?.acceptProxyProtocol &&
    !mask?.tcp?.length &&
    !mask?.udp?.length &&
    value.quicParams == null &&
    value.downloadSettings == null &&
    value.address == null &&
    value.port == null
  );
}

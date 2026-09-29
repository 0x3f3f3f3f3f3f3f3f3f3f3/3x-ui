import { z } from 'zod';
import { MieruHostSchema, MieruProfileSchema } from '@/schemas/mieru-profile';
import {
  MieruCredentialsSchema,
  MieruInboundSettingsSchema,
} from '@/schemas/protocols/inbound/mieru';
import { PortSchema } from '@/schemas/primitives/port';

export function genMieruLink(
  address: string,
  port: number,
  network: string,
  email: string,
  password: string,
  remark: string,
): string {
  const host = address.replace(/^\[|\]$/g, '');
  if (
    !MieruHostSchema.safeParse(host).success ||
    !PortSchema.safeParse(port).success ||
    !MieruCredentialsSchema.safeParse({ email, password }).success ||
    !MieruInboundSettingsSchema.shape.network.safeParse(network).success
  )
    return '';
  const url = new URL(`mierus://${host.includes(':') ? `[${host}]` : host}`);
  url.username = email;
  url.password = password;
  url.searchParams.set('profile', remark || 'mieru');
  if (network !== 'udp') url.searchParams.set('multiplexing', 'MULTIPLEXING_OFF');
  for (const transport of network === 'both' ? ['TCP', 'UDP'] : [network.toUpperCase()]) {
    url.searchParams.append('port', String(port));
    url.searchParams.append('protocol', transport);
  }
  url.searchParams.sort();
  return url.toString();
}

export function mieruConfigFromLink(link: string): string | null {
  if (!link.startsWith('mierus://')) return null;
  try {
    const url = new URL(link);
    const query = url.searchParams;
    if (
      url.port ||
      url.pathname ||
      url.hash ||
      [...query.keys()].some(
        (key) => !['profile', 'port', 'protocol', 'multiplexing'].includes(key),
      ) ||
      query.getAll('profile').length !== 1 ||
      query.getAll('multiplexing').length > 1 ||
      (query.has('multiplexing') && query.get('multiplexing') !== 'MULTIPLEXING_OFF')
    )
      return null;
    const ports = query.getAll('port');
    const protocols = query.getAll('protocol');
    if (ports.length !== protocols.length || ports.some((port) => !/^\d+$/.test(port))) return null;
    const host = url.hostname.replace(/^\[|\]$/g, '');
    const isIP = z.union([z.ipv4(), z.ipv6()]).safeParse(host).success;
    const result = MieruProfileSchema.safeParse({
      profileName: query.get('profile'),
      user: { name: decodeURIComponent(url.username), password: decodeURIComponent(url.password) },
      ...(protocols.includes('TCP') || query.has('multiplexing')
        ? { multiplexing: { level: 'MULTIPLEXING_OFF' } }
        : {}),
      servers: [
        {
          [isIP ? 'ipAddress' : 'domainName']: host,
          portBindings: ports.map((port, i) => ({ port: Number(port), protocol: protocols[i] })),
        },
      ],
    });
    if (!result.success) return null;
    return JSON.stringify(
      {
        profiles: [result.data],
        activeProfile: result.data.profileName,
        socks5Port: 1080,
        socks5ListenLAN: false,
      },
      null,
      2,
    );
  } catch {
    return null;
  }
}

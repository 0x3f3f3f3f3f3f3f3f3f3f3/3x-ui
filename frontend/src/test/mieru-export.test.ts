import { describe, expect, it } from 'vitest';
import { genAllLinks, genInboundLinks } from '@/lib/xray/inbound-link';
import { parseLinkParts } from '@/lib/xray/link-label';
import { InboundSchema } from '@/schemas/api/inbound';

describe('mieru share exports', () => {
  it.each(['tcp', 'udp', 'both'])(
    'exports native %s bindings with escaped credentials',
    (network) => {
      const inbound = InboundSchema.parse({
        protocol: 'mieru',
        port: 8443,
        shareAddrStrategy: 'custom',
        shareAddr: '[2001:db8::17]',
        settings: {
          network,
          bridgePort: 47221,
          clients: [{ email: 'native-user', password: 'fixture:p@ss/#?中文', totalGB: 12345 }],
        },
      });
      const link = genInboundLinks({
        inbound,
        remark: 'native profile',
        fallbackHostname: 'wrong.example',
      });
      expect(link.startsWith('mierus://')).toBe(true);
      const parsed = new URL(link);
      expect(decodeURIComponent(parsed.username)).toBe('native-user');
      expect(decodeURIComponent(parsed.password)).toBe('fixture:p@ss/#?中文');
      expect(parsed.hostname).toBe('[2001:db8::17]');
      expect(parsed.searchParams.get('profile')).toBe('native profile');
      expect(parsed.searchParams.getAll('protocol')).toEqual(
        network === 'both' ? ['TCP', 'UDP'] : [network.toUpperCase()],
      );
      expect(parsed.searchParams.getAll('port')).toEqual(
        network === 'both' ? ['8443', '8443'] : ['8443'],
      );
      expect(
        [...parsed.searchParams.keys()].every((key) =>
          ['profile', 'protocol', 'port'].includes(key),
        ),
      ).toBe(true);
    },
  );

  it('uses a native public endpoint override without Xray TLS fields', () => {
    const inbound = InboundSchema.parse({
      protocol: 'mieru',
      port: 8443,
      settings: { network: 'udp', clients: [] },
      streamSettings: {
        network: 'tcp',
        tcpSettings: {},
        security: 'none',
        externalProxy: [{ dest: 'edge.example', port: 9443, forceTls: 'tls', remark: 'edge' }],
      },
    });
    const [{ link }] = genAllLinks({
      inbound,
      client: { email: 'native-user', password: 'fixture-password' },
      remark: 'native',
      fallbackHostname: 'wrong.example',
    });
    const parsed = new URL(link);
    expect(parsed.hostname).toBe('edge.example');
    expect(parsed.searchParams.get('port')).toBe('9443');
    expect(parseLinkParts(link)).toEqual({
      protocol: 'mieru',
      network: 'UDP',
      security: '',
      remark: 'native-edge',
      port: '9443',
    });
  });
});

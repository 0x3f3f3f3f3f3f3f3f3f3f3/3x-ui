import { describe, expect, it } from 'vitest';
import { genLink, getInboundClients } from '@/lib/xray/inbound-link';
import { inboundFromDb } from '@/lib/xray/inbound-from-db';
import { parseOutboundLink } from '@/lib/xray/outbound-link-parser';
import { parseLinkParts } from '@/lib/xray/link-label';

describe('official mieru simple links', () => {
  it('reimports the official client JSON without silently selecting a profile or dropping native settings', () => {
    const profile = {
      profileName: 'native UDP',
      user: { name: '用户', password: '密码' },
      servers: [{ ipAddress: '2001:db8::1', portBindings: [{ port: 8443, protocol: 'UDP' }] }],
      mtu: 1400,
      multiplexing: { level: 'MULTIPLEXING_HIGH' },
    };
    const config = { profiles: [profile], activeProfile: 'native UDP', socks5Port: 1080 };
    expect(parseOutboundLink(JSON.stringify(config))).toMatchObject({
      protocol: 'mieru',
      tag: 'native UDP',
      settings: {
        address: '2001:db8::1',
        port: 8443,
        username: '用户',
        password: '密码',
        transport: 'UDP',
        mtu: 1400,
        multiplexing: 'MULTIPLEXING_HIGH',
      },
    });
    expect(
      parseOutboundLink(
        JSON.stringify({ ...config, profiles: [profile, { ...profile, profileName: 'second' }] }),
      ),
    ).toBeNull();
    expect(
      parseOutboundLink(
        JSON.stringify({ ...config, profiles: [{ ...profile, trafficPattern: {} }] }),
      ),
    ).toBeNull();
  });
  it('exports native credentials, IPv6 and UDP from the existing share generator', () => {
    const client = {
      email: 'label',
      mieruUsername: '用户:@%',
      mieruPassword: '密码:/?%',
      password: 'other-password',
    };
    const inbound = inboundFromDb({
      protocol: 'mieru',
      port: 8443,
      listen: '::',
      settings: { transport: 'UDP', mtu: 1400, clients: [client] },
      streamSettings: {},
      sniffing: {},
    });
    expect(getInboundClients(inbound)).toMatchObject([client]);
    const link = genLink({ inbound, client, address: '2001:db8::1', remark: 'native profile' });
    const url = new URL(link);
    expect(url.protocol).toBe('mierus:');
    expect(decodeURIComponent(url.username)).toBe(client.mieruUsername);
    expect(decodeURIComponent(url.password)).toBe(client.mieruPassword);
    expect(url.hostname).toBe('[2001:db8::1]');
    expect(url.searchParams.get('profile')).toBe('native profile');
    expect(url.searchParams.get('protocol')).toBe('UDP');
    expect(url.searchParams.get('port')).toBe('8443');
    expect(url.searchParams.get('mtu')).toBe('1400');
    expect(url.searchParams.get('multiplexing')).toBe('MULTIPLEXING_LOW');
    expect(link).not.toContain(client.password);
    expect(parseLinkParts(link)).toMatchObject({
      protocol: 'Mieru',
      network: 'UDP',
      security: '',
      remark: 'native profile',
      port: '8443',
    });
  });

  it('imports official simple URLs as native outbound settings with their profile choices', () => {
    const link =
      'mierus://%E7%94%A8%E6%88%B7%3A%40%25:%E5%AF%86%E7%A0%81%3A%2F%3F%25@[2001:db8::1]?mtu=1400&multiplexing=MULTIPLEXING_HIGH&port=8443&profile=native+profile&protocol=UDP';
    expect(parseOutboundLink(link)).toMatchObject({
      protocol: 'mieru',
      tag: 'native profile',
      settings: {
        address: '2001:db8::1',
        port: 8443,
        transport: 'UDP',
        mtu: 1400,
        username: '用户:@%',
        password: '密码:/?%',
        multiplexing: 'MULTIPLEXING_HIGH',
      },
    });
  });

  it('rejects native options that cannot be represented by the selected outbound', () => {
    const base = 'mierus://user:secret@native.example.test?profile=p&protocol=TCP&port=8443';
    expect(parseOutboundLink(base)).not.toBeNull();
    for (const extra of [
      '&port=9443&protocol=UDP',
      '&multiplexing=unknown',
      '&traffic-pattern=YWJj',
      '&handshake-mode=HANDSHAKE_MODE_NO_WAIT',
      '&mtu=1400&mtu=1500',
      '&unknown-option=required',
    ]) {
      expect(parseOutboundLink(base + extra)).toBeNull();
    }
  });
});

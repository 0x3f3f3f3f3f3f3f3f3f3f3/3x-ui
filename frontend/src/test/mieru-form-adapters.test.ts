import { describe, expect, it } from 'vitest';

import { createDefaultInboundSettings } from '@/lib/xray/inbound-defaults';
import { createDefaultOutboundSettings } from '@/lib/xray/outbound-defaults';
import {
  rawInboundToFormValues,
  formValuesToWirePayload as inboundToWire,
} from '@/lib/xray/inbound-form-adapter';
import {
  rawOutboundToFormValues,
  formValuesToWirePayload as outboundToWire,
} from '@/lib/xray/outbound-form-adapter';
import { canEnableReality, canEnableStream, canEnableTls } from '@/lib/xray/protocol-capabilities';
import { ProtocolSchema } from '@/schemas/primitives/protocol';
import { InboundFormSchema } from '@/schemas/forms/inbound-form';
import { OutboundFormSchema } from '@/schemas/forms/outbound-form';
import { ClientFormSchema } from '@/schemas/client';

describe('native mieru form contracts', () => {
  it('rejects TLS, wrapped transports and global mux for native profiles', () => {
    const raw = {
      protocol: 'mieru',
      settings: { address: 'example.test', port: 8443, username: 'user', password: 'secret' },
    };
    for (const streamSettings of [
      { network: 'kcp', security: 'none' },
      { network: 'tcp', security: 'reality' },
    ]) {
      expect(
        OutboundFormSchema.safeParse(rawOutboundToFormValues({ ...raw, streamSettings })).success,
      ).toBe(false);
    }
    expect(
      OutboundFormSchema.safeParse(rawOutboundToFormValues({ ...raw, mux: { enabled: true } }))
        .success,
    ).toBe(false);
    const inbound = rawInboundToFormValues({
      protocol: 'mieru',
      port: 8443,
      settings: '{"clients":[]}',
      streamSettings: '{"network":"kcp","security":"none"}',
    });
    expect(InboundFormSchema.safeParse(inbound).success).toBe(false);
  });
  it('offers empty authenticated native listener defaults', () => {
    expect(ProtocolSchema.safeParse('mieru').success).toBe(true);
    expect(createDefaultInboundSettings('mieru')).toMatchObject({ transport: 'TCP', clients: [] });
    expect(createDefaultOutboundSettings('mieru')).toMatchObject({
      transport: 'TCP',
      multiplexing: 'MULTIPLEXING_LOW',
    });
    expect(canEnableTls({ protocol: 'mieru' })).toBe(false);
    expect(canEnableReality({ protocol: 'mieru' })).toBe(false);
    expect(canEnableStream({ protocol: 'mieru' })).toBe(false);
  });

  it('preserves native credentials, options and canonical client labels on inbound reopen/save', () => {
    const settings = {
      transport: 'UDP',
      mtu: 1400,
      maxConnections: 32,
      handshakeTimeoutSeconds: 8,
      userHintRequired: true,
      clients: [
        {
          email: 'label',
          enable: true,
          mieruUsername: '独立-user',
          mieruPassword: '独立-password',
          password: 'other-protocol',
        },
      ],
    };
    const values = rawInboundToFormValues({
      protocol: 'mieru',
      port: 443,
      settings: JSON.stringify(settings),
      streamSettings: '{}',
      sniffing: '{}',
    });
    const parsed = InboundFormSchema.safeParse(values);
    expect(parsed.success).toBe(true);
    if (!parsed.success) return;
    const wire = inboundToWire(parsed.data);
    expect(JSON.parse(wire.settings)).toMatchObject(settings);
    expect(JSON.parse(wire.settings)).not.toHaveProperty('users');
  });

  it('round trips native outbound TCP/UDP and official multiplexing without global mux', () => {
    for (const transport of ['TCP', 'UDP']) {
      const settings = {
        address: '2001:db8::1',
        port: 8443,
        transport,
        mtu: 1400,
        username: '独立-user',
        password: '独立-password',
        multiplexing: 'MULTIPLEXING_HIGH',
      };
      const form = rawOutboundToFormValues({ protocol: 'mieru', tag: 'native', settings });
      const parsed = OutboundFormSchema.safeParse(form);
      expect(parsed.success).toBe(true);
      if (!parsed.success) continue;
      const wire = outboundToWire(parsed.data);
      expect(wire).toMatchObject({ protocol: 'mieru', tag: 'native', settings });
      expect(wire).not.toHaveProperty('mux');
    }
  });

  it('accepts 64 UTF-8 credential bytes and rejects 65 bytes and unpaired surrogates', () => {
    const fields = ClientFormSchema.pick({ email: true, password: true }).extend({});
    const nativeFields = ClientFormSchema.partial();
    const raw = {
      email: 'label',
      password: 'other',
      mieruUsername: '界'.repeat(21) + 'a',
      mieruPassword: 'secret',
    };
    expect(fields.parse(raw)).toMatchObject({ email: 'label', password: 'other' });
    const accepted = nativeFields.safeParse(raw);
    expect(accepted.success).toBe(true);
    if (accepted.success)
      expect(accepted.data).toMatchObject({
        mieruUsername: raw.mieruUsername,
        mieruPassword: 'secret',
      });
    expect(nativeFields.safeParse({ ...raw, mieruUsername: '界'.repeat(21) + 'ab' }).success).toBe(
      false,
    );
    expect(nativeFields.safeParse({ ...raw, mieruPassword: '\ud800' }).success).toBe(false);
  });
});

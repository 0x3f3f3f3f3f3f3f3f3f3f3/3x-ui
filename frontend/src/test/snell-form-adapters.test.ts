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
import { InboundFormSchema } from '@/schemas/forms/inbound-form';
import { OutboundFormSchema } from '@/schemas/forms/outbound-form';
import { ClientFormSchema } from '@/schemas/client';
import { ProtocolSchema } from '@/schemas/primitives/protocol';

const owner = {
  email: 'native-owner',
  enable: true,
  snellPsk: 'independent-native-snell-psk',
  password: 'ordinary-kept',
  sshPassword: 'ssh-kept',
  mieruPassword: 'mieru-kept',
};
const endpoint = { address: '2001:db8::1', port: 443, psk: '独立,quoted"\\native-key' };

describe('native Snell form adapters', () => {
  it('offers explicit version4 defaults and preserves independent canonical PSK', () => {
    expect(ProtocolSchema.safeParse('snell').success).toBe(true);
    expect(createDefaultInboundSettings('snell')).toMatchObject({ version: 4, clients: [] });
    expect(createDefaultOutboundSettings('snell')).toMatchObject({
      version: 4,
      address: '',
      psk: '',
    });
    const client = ClientFormSchema.partial().parse({ ...owner, inboundIds: [] });
    expect(client).toMatchObject(owner);
    expect(
      ClientFormSchema.partial().safeParse({ ...owner, snellPsk: 'x'.repeat(256) }).success,
    ).toBe(false);
    expect(
      ClientFormSchema.partial().safeParse({ ...owner, snellPsk: '界'.repeat(86) }).success,
    ).toBe(false);
  });

  it.each([4, 5, 6])(
    'reopens version%d native ownership without changing credentials',
    (version) => {
      const values = rawInboundToFormValues({
        protocol: 'snell',
        port: 443,
        enable: true,
        settings: {
          version,
          clients: [owner],
          obfs: 'off',
          mode: version === 6 ? 'unshaped' : '',
          quic: false,
        },
        streamSettings: { network: 'raw', security: 'none' },
      });
      const parsed = InboundFormSchema.safeParse(values);
      expect(parsed.success).toBe(true);
      if (!parsed.success) return;
      const payload = inboundToWire(parsed.data);
      expect(payload.protocol).toBe('snell');
      expect(JSON.parse(payload.settings)).toMatchObject({ version, clients: [owner] });
    },
  );

  it('keeps explicit outbound version/endpoint/PSK and native v5 QUIC options', () => {
    const settings = {
      ...endpoint,
      version: 5,
      obfs: 'http',
      obfsHost: 'snell.example',
      obfsUri: '/native',
      mode: '',
      reuse: true,
      quic: true,
    };
    const values = rawOutboundToFormValues({ protocol: 'snell', tag: 'native', settings });
    expect(values.protocol).toBe('snell');
    const parsed = OutboundFormSchema.safeParse(values);
    expect(parsed.success).toBe(true);
    if (!parsed.success) return;
    expect(outboundToWire(parsed.data)).toMatchObject({
      protocol: 'snell',
      tag: 'native',
      settings,
    });
  });

  it('rejects wrappers, global mux and invalid version combinations before submission', () => {
    for (const streamSettings of [
      { network: 'kcp' },
      { network: 'tcp', security: 'tls' },
      { network: 'tcp', finalmask: { tcp: [{}] } },
    ]) {
      expect(
        InboundFormSchema.safeParse(
          rawInboundToFormValues({
            protocol: 'snell',
            port: 443,
            settings: { version: 5, clients: [owner] },
            streamSettings,
          }),
        ).success,
      ).toBe(false);
      expect(
        OutboundFormSchema.safeParse(
          rawOutboundToFormValues({
            protocol: 'snell',
            settings: { ...endpoint, version: 5 },
            streamSettings,
          }),
        ).success,
      ).toBe(false);
    }
    expect(
      OutboundFormSchema.safeParse(
        rawOutboundToFormValues({
          protocol: 'snell',
          settings: { ...endpoint, version: 5 },
          mux: { enabled: true, concurrency: 8 },
        }),
      ).success,
    ).toBe(false);
    for (const options of [
      { version: 3 },
      { version: 6, quic: true },
      { version: 6, obfs: 'http' },
      { version: 6, mode: 'unsafe-raw' },
      { version: 4, mode: 'default' },
    ]) {
      expect(
        OutboundFormSchema.safeParse(
          rawOutboundToFormValues({ protocol: 'snell', settings: { ...endpoint, ...options } }),
        ).success,
      ).toBe(false);
    }
  });

  it('requires exactly one owner for enabled resources and accepts disabled staging', () => {
    expect(
      InboundFormSchema.safeParse(
        rawInboundToFormValues({
          protocol: 'snell',
          port: 443,
          enable: true,
          settings: { version: 4, clients: [] },
        }),
      ).success,
    ).toBe(false);
    expect(
      InboundFormSchema.safeParse(
        rawInboundToFormValues({
          protocol: 'snell',
          port: 443,
          enable: false,
          settings: { version: 4, clients: [] },
        }),
      ).success,
    ).toBe(true);
    expect(
      InboundFormSchema.safeParse(
        rawInboundToFormValues({
          protocol: 'snell',
          port: 443,
          settings: { version: 5, clients: [owner, { ...owner, email: 'second' }] },
        }),
      ).success,
    ).toBe(false);
    expect(
      InboundFormSchema.safeParse(
        rawInboundToFormValues({
          protocol: 'snell',
          port: 443,
          settings: { version: 6, clients: [{ ...owner, snellPsk: 'short' }] },
        }),
      ).success,
    ).toBe(false);
  });
});

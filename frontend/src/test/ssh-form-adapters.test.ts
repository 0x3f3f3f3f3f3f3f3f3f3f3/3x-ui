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

export const SSH_PUBLIC_KEY =
  'ssh-ed25519 ' +
  btoa(
    String.fromCharCode(0, 0, 0, 11) +
      'ssh-ed25519' +
      String.fromCharCode(0, 0, 0, 32) +
      'k'.repeat(32),
  );
const outbound = {
  address: '2001:db8::1',
  port: 2222,
  username: '独立-user',
  password: '独立-secret',
  privateKeyFile: '',
  hostKey: SSH_PUBLIC_KEY,
  handshakeTimeoutSeconds: 10,
  idleTimeoutSeconds: 300,
};

describe('native SSH form contracts', () => {
  it('keeps the existing transport validation for other protocols', () => {
    for (const streamSettings of [{ network: 'raw', security: 'none' }, { security: '' }]) {
      expect(
        InboundFormSchema.safeParse(
          rawInboundToFormValues({
            protocol: 'vless',
            port: 443,
            settings: createDefaultInboundSettings('vless'),
            streamSettings,
          }),
        ).success,
      ).toBe(false);
      expect(
        OutboundFormSchema.safeParse(
          rawOutboundToFormValues({ protocol: 'freedom', settings: {}, streamSettings }),
        ).success,
      ).toBe(false);
    }
  });
  it('accepts native empty stream defaults and rejects listener port ranges before submission', () => {
    for (const streamSettings of [
      { network: '', security: '' },
      { network: 'raw', security: '' },
    ]) {
      expect(
        InboundFormSchema.safeParse(
          rawInboundToFormValues({
            protocol: 'ssh',
            port: 2222,
            settings: { clients: [] },
            streamSettings,
          }),
        ).success,
      ).toBe(true);
      expect(
        OutboundFormSchema.safeParse(
          rawOutboundToFormValues({ protocol: 'ssh', settings: outbound, streamSettings }),
        ).success,
      ).toBe(true);
    }
    for (const port of ['2200-2201', 0, 65536]) {
      expect(
        InboundFormSchema.safeParse({
          ...rawInboundToFormValues({ protocol: 'ssh', port: 2222, settings: { clients: [] } }),
          port,
        }).success,
      ).toBe(false);
    }
  });
  it('offers a key-only listener with bounded native defaults', () => {
    expect(ProtocolSchema.safeParse('ssh').success).toBe(true);
    expect(createDefaultInboundSettings('ssh')).toMatchObject({
      clients: [],
      allowPassword: false,
      maxConnections: 64,
      reverse: { enabled: false },
    });
    expect(createDefaultOutboundSettings('ssh')).toMatchObject({
      port: 22,
      hostKey: '',
      privateKeyFile: '',
      password: '',
    });
  });
  it('preserves clients and reverse permissions on listener reopen and clone', () => {
    const settings = {
      ...(createDefaultInboundSettings('ssh') ?? {}),
      clients: [
        {
          email: 'label',
          enable: true,
          sshUsername: '独立-user',
          sshAuthorizedKeys: SSH_PUBLIC_KEY,
          sshPassword: '',
          password: 'other',
        },
      ],
      reverse: {
        enabled: true,
        bindAddresses: ['127.0.0.1'],
        portFrom: 22000,
        portTo: 22010,
        sourceCIDRs: ['127.0.0.0/8'],
        maxListeners: 2,
        allowPortZero: false,
      },
    };
    const parsed = InboundFormSchema.safeParse(
      rawInboundToFormValues({ protocol: 'ssh', port: 2222, settings, streamSettings: {} }),
    );
    expect(parsed.success).toBe(true);
    if (!parsed.success) return;
    const wire = inboundToWire(parsed.data);
    expect(JSON.parse(wire.settings)).toMatchObject(settings);
    expect(JSON.parse(wire.settings)).not.toHaveProperty('hostKeyFile');
    expect(wire).not.toHaveProperty('sshHostKeyId');
  });
  it('round trips strict pinned password and business-key outbounds', () => {
    for (const auth of [
      { password: 'secret', privateKeyFile: '' },
      { password: '', privateKeyFile: '/var/lib/x-ui/native-ssh/outbound/user.pem' },
    ]) {
      const settings = { ...outbound, ...auth };
      const parsed = OutboundFormSchema.safeParse(
        rawOutboundToFormValues({ protocol: 'ssh', tag: 'native', settings }),
      );
      expect(parsed.success).toBe(true);
      if (parsed.success)
        expect(outboundToWire(parsed.data)).toMatchObject({ protocol: 'ssh', settings });
    }
    for (const patch of [
      { hostKey: '' },
      { hostKey: 'SHA256:fingerprint-only' },
      { password: '', privateKeyFile: '' },
    ]) {
      expect(
        OutboundFormSchema.safeParse(
          rawOutboundToFormValues({ protocol: 'ssh', settings: { ...outbound, ...patch } }),
        ).success,
      ).toBe(false);
    }
  });
  it('rejects raw users, native option overflow and unsupported UDP/wrappers/mux', () => {
    for (const streamSettings of [
      { network: 'kcp', security: 'none' },
      { network: 'tcp', security: 'tls' },
      { network: 'tcp', security: 'none', tcpSettings: { header: { type: 'http' } } },
      { security: 'none', downloadSettings: { address: 'hidden' } },
    ]) {
      expect(
        OutboundFormSchema.safeParse(
          rawOutboundToFormValues({ protocol: 'ssh', settings: outbound, streamSettings }),
        ).success,
      ).toBe(false);
      expect(
        InboundFormSchema.safeParse(
          rawInboundToFormValues({
            protocol: 'ssh',
            port: 2222,
            settings: { clients: [] },
            streamSettings,
          }),
        ).success,
      ).toBe(false);
    }
    expect(
      OutboundFormSchema.safeParse(
        rawOutboundToFormValues({ protocol: 'ssh', settings: outbound, mux: { enabled: true } }),
      ).success,
    ).toBe(false);
    for (const settings of [
      { clients: [], users: [] },
      { clients: [], maxConnections: 1025 },
      {
        clients: [],
        reverse: {
          enabled: true,
          bindAddresses: ['127.0.0.1'],
          portFrom: 22010,
          portTo: 22000,
          sourceCIDRs: ['127.0.0.0/8'],
        },
      },
    ])
      expect(
        InboundFormSchema.safeParse(
          rawInboundToFormValues({ protocol: 'ssh', port: 2222, settings }),
        ).success,
      ).toBe(false);
  });
  it('validates independent UTF-8 bounds and option-free public keys', () => {
    const schema = ClientFormSchema.partial();
    const valid = {
      email: 'label',
      sshUsername: '界'.repeat(85) + 'a',
      sshPassword: '界'.repeat(341) + 'a',
      sshAuthorizedKeys: SSH_PUBLIC_KEY,
    };
    const parsed = schema.safeParse(valid);
    expect(parsed.success).toBe(true);
    if (parsed.success) expect(parsed.data).toMatchObject(valid);
    for (const patch of [
      { sshUsername: '界'.repeat(85) + 'ab' },
      { sshUsername: 'white space' },
      { sshPassword: '界'.repeat(341) + 'ab' },
      { sshPassword: '\ud800' },
      { sshAuthorizedKeys: 'command="bad" ' + SSH_PUBLIC_KEY },
      { sshAuthorizedKeys: Array(17).fill(SSH_PUBLIC_KEY).join('\n') },
    ])
      expect(schema.safeParse({ ...valid, ...patch }).success).toBe(false);
  });
});

import { describe, expect, it } from 'vitest';

import { formValuesToWirePayload, rawOutboundToFormValues } from '@/lib/xray/outbound-form-adapter';
import { createDefaultOutboundSettings } from '@/lib/xray/outbound-defaults';
import { PROTOCOL_OPTIONS } from '@/pages/xray/outbounds/outbound-form-constants';
import { effectiveTestMode, outboundAddresses } from '@/pages/xray/outbounds/outbounds-tab-helpers';
import { isUdpOutbound } from '@/hooks/useXraySetting';
import { OutboundFormSettingsSchema } from '@/schemas/forms/outbound-form';
import { OutboundSettingsSchema } from '@/schemas/protocols/outbound';
import { SSH_OUTBOUND_HOST_KEY, SSH_OUTBOUND_PRIVATE_KEY } from './ssh-outbound-fixture';

const settings = {
  address: 'ssh.example.net',
  port: 22,
  user: 'forward',
  privateKey: SSH_OUTBOUND_PRIVATE_KEY,
  hostKey: SSH_OUTBOUND_HOST_KEY,
};

describe('SSH outbound form', () => {
  it('offers SSH with port 22 and requires administrator-provided credentials', () => {
    expect(PROTOCOL_OPTIONS).toContainEqual({ value: 'ssh', label: 'ssh' });
    expect(createDefaultOutboundSettings('ssh')).toEqual({
      address: '',
      port: 22,
      user: '',
      privateKey: '',
      hostKey: '',
    });
  });

  it('recognizes the typed settings in both wire and form schemas', () => {
    const value = { protocol: 'ssh', settings };
    expect(OutboundSettingsSchema.safeParse(value).success).toBe(true);
    expect(OutboundFormSettingsSchema.safeParse(value).success).toBe(true);
  });

  it('displays the SSH endpoint and labels TCP-mode checks as routed HTTP', () => {
    const row = { key: 0, protocol: 'SSH', tag: 'ssh-exit', settings };
    expect(outboundAddresses(row)).toEqual(['ssh.example.net:22']);
    expect(effectiveTestMode(row, 'tcp')).toBe('http');
    expect(effectiveTestMode(row, 'real')).toBe('real');
    expect(isUdpOutbound(row)).toBe(false);
  });

  it.each(['ssh', 'SSH', 'Ssh'])(
    'preserves multiline private keys and exact passphrases for %s',
    (protocol) => {
      const authored = {
        protocol,
        tag: 'ssh-exit',
        settings: { ...settings, privateKeyPassphrase: ' exact phrase ' },
      };
      const form = rawOutboundToFormValues(authored);
      expect(form.protocol).toBe('ssh');
      expect(formValuesToWirePayload(form)).toEqual({ ...authored, protocol: 'ssh' });
    },
  );

  it('emits only the SSH envelope despite stale native controls', () => {
    const form = rawOutboundToFormValues({
      tag: 'ssh-exit',
      protocol: 'ssh',
      settings,
      sendThrough: '192.0.2.1',
      targetStrategy: 'UseIP',
      streamSettings: { network: 'tcp', sockopt: { dialerProxy: 'other-exit' } },
      mux: { enabled: true, concurrency: 8 },
    });
    expect(formValuesToWirePayload(form)).toEqual({ tag: 'ssh-exit', protocol: 'ssh', settings });
  });

  it.each([
    { name: 'empty account', patch: { user: '' } },
    { name: 'padded account', patch: { user: ' forward' } },
    { name: 'control in account', patch: { user: 'for\nward' } },
    { name: 'account exceeds UTF-8 byte limit', patch: { user: '界'.repeat(86) } },
    { name: 'empty private key', patch: { privateKey: '' } },
    { name: 'oversized private key', patch: { privateKey: 'a'.repeat(65537) } },
    {
      name: 'passphrase exceeds UTF-8 byte limit',
      patch: { privateKeyPassphrase: '界'.repeat(1366) },
    },
    { name: 'missing host pin', patch: { hostKey: '' } },
    {
      name: 'multiple host pins',
      patch: { hostKey: SSH_OUTBOUND_HOST_KEY + '\n' + SSH_OUTBOUND_HOST_KEY },
    },
    { name: 'zero port', patch: { port: 0 } },
    { name: 'oversized port', patch: { port: 65536 } },
    { name: 'fractional port', patch: { port: 22.5 } },
    { name: 'missing address', patch: { address: '' } },
    { name: 'address with URL scheme', patch: { address: 'ssh://example.net' } },
    { name: 'address with port', patch: { address: 'example.net:22' } },
    { name: 'wildcard upstream', patch: { address: '*' } },
  ])('rejects $name', ({ patch }) => {
    expect(
      OutboundSettingsSchema.safeParse({ protocol: 'ssh', settings: { ...settings, ...patch } })
        .success,
    ).toBe(false);
  });

  it('rejects unsupported SSH settings instead of silently dropping them', () => {
    expect(
      OutboundSettingsSchema.safeParse({
        protocol: 'ssh',
        settings: { ...settings, password: 'unsupported' },
      }).success,
    ).toBe(false);
  });

  it.each(['example.net', '192.0.2.1', '::1', '[2001:db8::1]'])(
    'accepts the upstream host %s',
    (address) => {
      expect(
        OutboundSettingsSchema.safeParse({ protocol: 'ssh', settings: { ...settings, address } })
          .success,
      ).toBe(true);
    },
  );
});

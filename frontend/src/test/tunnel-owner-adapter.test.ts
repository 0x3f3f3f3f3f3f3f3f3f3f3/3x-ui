import { describe, expect, it } from 'vitest';
import { DBInbound } from '@/models/dbinbound';
import { rawInboundToFormValues, formValuesToWirePayload } from '@/lib/xray/inbound-form-adapter';
import { InboundFormSchema } from '@/schemas/forms/inbound-form';

const owner = 'a6426bfc-42c6-45d6-8182-1108f7986d89';

describe('Tunnel ownership editing', () => {
  it.each([null, owner])(
    'edits a detached legacy listener whose clients are null (owner %s)',
    (ownerClientId) => {
      const parsed = InboundFormSchema.parse(
        rawInboundToFormValues({
          protocol: 'tunnel',
          port: 24101,
          ownerClientId,
          settings: { clients: null },
        }),
      );
      const payload = formValuesToWirePayload(parsed);
      expect(payload.ownerClientId).toBe(ownerClientId ?? undefined);
      expect(JSON.parse(payload.settings).clients).toBe(ownerClientId ? undefined : null);
    },
  );
  it('carries the stable owner through the list model and sends only its identity', () => {
    const row = new DBInbound({
      protocol: 'tunnel',
      port: 24101,
      ownerClientId: owner,
      settings: {
        rewriteAddress: '127.0.0.1',
        rewritePort: 80,
        clients: [{ email: 'old-name', totalGB: 1 }],
      },
      clientStats: [{ email: 'old-name', up: 123, down: 456, total: 1000, expiryTime: 0 }],
    });
    const parsed = InboundFormSchema.parse(rawInboundToFormValues(row));
    const payload = formValuesToWirePayload(parsed);
    expect(payload.ownerClientId).toBe(owner);
    expect(JSON.parse(payload.settings).clients).toBeUndefined();
    expect(payload.clientStats).toBeUndefined();
  });

  it('preserves legacy ownership without an explicit owner command', () => {
    const clients = [
      { email: 'legacy-owner', enable: true, totalGB: 123, policy: { multiplier: '2' } },
    ];
    const parsed = InboundFormSchema.parse(
      rawInboundToFormValues({
        protocol: 'tunnel',
        port: 24101,
        settings: { clients },
      }),
    );
    const payload = formValuesToWirePayload(parsed);
    expect(payload.ownerClientId).toBeUndefined();
    expect(JSON.parse(payload.settings).clients).toEqual(clients);
  });

  it.each(['remote', 'different-protocol'])('omits a hidden owner command for %s', (scenario) => {
    const parsed = InboundFormSchema.parse(
      rawInboundToFormValues({
        protocol: scenario === 'remote' ? 'tunnel' : 'vless',
        port: 24101,
        nodeId: scenario === 'remote' ? 7 : null,
        ownerClientId: owner,
        settings: { clients: [] },
      }),
    );
    expect(formValuesToWirePayload(parsed).ownerClientId).toBeUndefined();
  });
});

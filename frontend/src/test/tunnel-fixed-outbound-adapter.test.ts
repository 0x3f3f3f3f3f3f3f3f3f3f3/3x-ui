import { describe, expect, it } from 'vitest';
import { formValuesToWirePayload, rawInboundToFormValues } from '@/lib/xray/inbound-form-adapter';
import { InboundFormSchema } from '@/schemas/forms/inbound-form';

function raw(outboundTag: unknown) {
  return rawInboundToFormValues({
    protocol: 'tunnel',
    port: 24101,
    settings: {
      allowedNetwork: 'tcp,udp',
      rewriteAddress: '127.0.0.1',
      rewritePort: 80,
      outboundTag,
    },
  });
}

describe('Tunnel fixed outbound editing', () => {
  it.each(['direct', 'removed-subscription', '', null, undefined])(
    'preserves the saved choice or explicit routing mode: %s',
    (outboundTag) => {
      const payload = formValuesToWirePayload(InboundFormSchema.parse(raw(outboundTag)));
      expect(JSON.parse(payload.settings).outboundTag).toBe(outboundTag);
    },
  );
  it.each([3, true, ['direct'], { tag: 'direct' }].map((tag) => ({ tag })))(
    'rejects malformed selection: $tag',
    ({ tag }) => {
      expect(InboundFormSchema.safeParse(raw(tag)).success).toBe(false);
    },
  );
});

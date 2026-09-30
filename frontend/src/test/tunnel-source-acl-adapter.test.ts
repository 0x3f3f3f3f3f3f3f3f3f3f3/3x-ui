import { describe, expect, it } from 'vitest';
import { rawInboundToFormValues, formValuesToWirePayload } from '@/lib/xray/inbound-form-adapter';
import { InboundFormSchema } from '@/schemas/forms/inbound-form';

const owner = 'a6426bfc-42c6-45d6-8182-1108f7986d89';
function raw(allowedSourceCidrs: unknown) {
  return rawInboundToFormValues({
    protocol: 'tunnel',
    port: 24101,
    ownerClientId: owner,
    settings: {
      allowedNetwork: 'tcp,udp',
      rewriteAddress: '127.0.0.1',
      rewritePort: 80,
      allowedSourceCidrs,
    },
  });
}
describe('Tunnel source ACL editing', () => {
  it.each(
    [null, [], ['192.0.2.0/24', '2001:db8::/32', '::fffe:c000:201/128', '::/0']].map(
      (prefixes) => ({ prefixes }),
    ),
  )(
    'preserves configured prefixes and explicit clearing: $prefixes',
    ({ prefixes: allowedSourceCidrs }) => {
      const payload = formValuesToWirePayload(InboundFormSchema.parse(raw(allowedSourceCidrs)));
      expect(JSON.parse(payload.settings).allowedSourceCidrs).toEqual(allowedSourceCidrs);
      expect(payload.ownerClientId).toBe(owner);
    },
  );
  it.each(
    [
      ['192.0.2.1'],
      ['127.0.0.1/33'],
      ['2001:db8::/129'],
      ['::ffff:192.0.2.1/128'],
      ['::ffff:c000:201/128'],
      ['0:0:0:0:0:ffff:c000:201/128'],
      [''],
      Array(257).fill('127.0.0.1/32'),
    ].map((prefixes) => ({ prefixes })),
  )('rejects invalid prefix inputs: $prefixes', ({ prefixes }) => {
    expect(InboundFormSchema.safeParse(raw(prefixes)).success).toBe(false);
  });
});

import { z } from 'zod';

export function snellPSKBytes(value: string): number {
  return new TextEncoder().encode(value).length;
}
export const SnellPSKSchema = z.string().refine(
  (value) =>
    !Array.from(value).some((char) => {
      const code = char.codePointAt(0)!;
      return code >= 0xd800 && code <= 0xdfff;
    }) && snellPSKBytes(value) <= 255,
  'pages.clients.snellPskInvalid',
);
export const SnellVersionSchema = z.union([z.literal(4), z.literal(5), z.literal(6)]);
export const SnellOptionFields = {
  version: SnellVersionSchema,
  obfs: z.enum(['', 'off', 'http']).default('off'),
  mode: z.enum(['', 'default', 'unshaped']).default(''),
  quic: z.boolean().default(false),
};
export function validateSnellOptions(
  value: { version: number; obfs: string; mode: string; quic: boolean },
  ctx: z.RefinementCtx,
): void {
  if (
    (value.quic && value.version !== 5) ||
    (value.version === 6 && value.obfs === 'http') ||
    (value.version !== 6 && value.mode !== '')
  )
    ctx.addIssue({ code: 'custom', message: 'pages.inbounds.form.snell.optionsInvalid' });
}

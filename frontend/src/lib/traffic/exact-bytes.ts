export function exactBytes(whole: string, milli = 0): string {
  const amount = BigInt(whole) * 1000n + BigInt(milli);
  if (amount <= 0n) return '0 B';
  const fraction = String(amount % 1000n)
    .padStart(3, '0')
    .replace(/0+$/, '');
  return `${amount / 1000n}${fraction ? `.${fraction}` : ''} B`;
}

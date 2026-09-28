import type { ClientBilling } from '@/schemas/client-policy';
import { exactBytes } from '@/lib/traffic/exact-bytes';

export function compactBilledBytes(whole: string, carry = 0): string {
  const value = BigInt(whole) * 1000n + BigInt(carry);
  if (value < 1024000n) return exactBytes(whole, carry);
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB', 'EiB'];
  let unit = 1000n;
  let index = 0;
  while (index < units.length - 1 && value >= unit * 1024n) {
    unit *= 1024n;
    index++;
  }
  const hundredths = (value * 100n + unit / 2n) / unit;
  const fraction = String(hundredths % 100n)
    .padStart(2, '0')
    .replace(/0+$/, '');
  return `${hundredths / 100n}${fraction ? `.${fraction}` : ''} ${units[index]}`;
}

export function clientBillingDisplay(
  billing: ClientBilling,
  trafficDiff = 0,
  enabled = true,
  isDark = false,
) {
  const used = BigInt(billing.billed) * 1000n + BigInt(billing.remainder);
  const quota = BigInt(billing.quota) * 1000n;
  const available = BigInt(billing.remaining) * 1000n - BigInt(billing.remainder);
  const remaining = available > 0n ? available : 0n;
  const percent = quota > 0n ? Number(used >= quota ? 10000n : (used * 10000n) / quota) / 100 : 100;
  const threshold =
    BigInt(Number.isFinite(trafficDiff) ? Math.max(0, Math.trunc(trafficDiff)) : 0) * 1000n;
  const nearLimit = !billing.unlimited && remaining < threshold;
  return {
    percent,
    nearLimit,
    isUnlimited: billing.unlimited,
    isDepleted: billing.exhausted,
    status: billing.exhausted && enabled ? ('exception' as const) : undefined,
    strokeColor: !enabled
      ? isDark
        ? 'rgb(72, 84, 105)'
        : '#bcbcbc'
      : billing.unlimited
        ? '#722ed1'
        : billing.exhausted
          ? '#ff4d4f'
          : nearLimit
            ? '#faad14'
            : '#389e0a',
    remainingColor: billing.unlimited
      ? 'purple'
      : billing.exhausted
        ? 'red'
        : used * 100n >= quota * 85n
          ? 'orange'
          : 'green',
    usedLabel: compactBilledBytes(billing.billed, billing.remainder),
    quotaLabel: compactBilledBytes(billing.quota),
    remainingLabel: compactBilledBytes(billing.remaining, -billing.remainder),
  };
}

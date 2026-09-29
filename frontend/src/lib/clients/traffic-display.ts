import { ColorUtils } from '@/utils';
import type { ClientPolicyAccounting } from '@/schemas/client';

export interface TrafficDisplayInput {
  up: number;
  down: number;
  total: number;
  enabled: boolean;
  trafficDiff: number;
  accounting?: ClientPolicyAccounting | null;
}

export interface TrafficDisplay {
  up: number;
  down: number;
  total: number;
  used: number;
  remaining: number;
  percent: number;
  isUnlimited: boolean;
  isDepleted: boolean;
  strokeColor: string;
  status: 'normal' | 'exception' | undefined;
}

const DISABLED_STROKE = {
  light: '#bcbcbc',
  dark: 'rgb(72, 84, 105)',
} as const;

const UNLIMITED_STROKE = '#722ed1';

export function computeTrafficDisplay(input: TrafficDisplayInput, isDark: boolean): TrafficDisplay {
  const accounting = input.accounting;
  const up = accounting ? Number(accounting.period.upload) : input.up || 0;
  const down = accounting ? Number(accounting.period.download) : input.down || 0;
  const used = accounting
    ? Number(accounting.period.billed) + Number(accounting.period.uncertain)
    : up + down;
  const total = accounting ? Number(accounting.quotaBytes) : input.total || 0;
  const isUnlimited = total <= 0;

  let percent = 100;
  if (!isUnlimited) {
    percent = Math.min(100, Math.max(0, (used / total) * 100));
  }

  const remaining = isUnlimited
    ? 0
    : accounting && accounting.remaining !== null
      ? Number(accounting.remaining)
      : Math.max(0, total - used);
  const isDepleted = !isUnlimited && remaining === 0;

  let strokeColor: string;
  if (!input.enabled) {
    strokeColor = isDark ? DISABLED_STROKE.dark : DISABLED_STROKE.light;
  } else if (isUnlimited) {
    strokeColor = UNLIMITED_STROKE;
  } else {
    strokeColor = ColorUtils.clientUsageColor({ up: used, down: 0, total }, input.trafficDiff);
  }

  return {
    up,
    down,
    total,
    used,
    remaining,
    percent,
    isUnlimited,
    isDepleted,
    strokeColor,
    status: isDepleted && input.enabled ? 'exception' : undefined,
  };
}

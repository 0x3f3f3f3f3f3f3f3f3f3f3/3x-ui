import { memo, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Popover, Progress } from 'antd';

import InfinityIcon from '@/components/ui/InfinityIcon';
import { useTheme } from '@/hooks/useTheme';
import { computeTrafficDisplay } from '@/lib/clients/traffic-display';
import { clientBillingDisplay } from '@/lib/clients/billing-display';
import { exactBytes } from '@/lib/traffic/exact-bytes';
import type { ClientBilling } from '@/schemas/client-policy';
import { SizeFormatter } from '@/utils';
import './ClientTrafficCell.css';

export interface ClientTrafficCellProps {
  up?: number;
  down?: number;
  total?: number;
  enabled?: boolean;
  trafficDiff?: number;
  compact?: boolean;
  billing?: ClientBilling | null;
}

// Query structural sharing keeps unchanged billing snapshots stable between polls.
const ClientTrafficCell = memo(function ClientTrafficCell({
  up = 0,
  down = 0,
  total = 0,
  enabled = true,
  trafficDiff = 0,
  compact = false,
  billing,
}: ClientTrafficCellProps) {
  const { t } = useTranslation();
  const { isDark } = useTheme();

  const rawDisplay = useMemo(
    () => computeTrafficDisplay({ up, down, total, enabled, trafficDiff }, isDark),
    [up, down, total, enabled, trafficDiff, isDark],
  );
  const charged = billing ? clientBillingDisplay(billing, trafficDiff, enabled, isDark) : null;
  const display = charged ?? rawDisplay;
  const usedLabel = charged?.usedLabel ?? SizeFormatter.sizeFormat(rawDisplay.used);
  const quotaLabel = charged?.quotaLabel ?? SizeFormatter.sizeFormat(total);

  const popover = (
    <table className="client-traffic-popover">
      <tbody>
        <tr>
          <td>↑</td>
          <td>{billing ? exactBytes(billing.up) : SizeFormatter.sizeFormat(up)}</td>
          <td>↓</td>
          <td>{billing ? exactBytes(billing.down) : SizeFormatter.sizeFormat(down)}</td>
        </tr>
        {!display.isUnlimited && (
          <tr>
            <td colSpan={2}>{t('remained')}</td>
            <td colSpan={2}>
              {billing
                ? exactBytes(billing.remaining, -billing.remainder)
                : SizeFormatter.sizeFormat(rawDisplay.remaining)}
            </td>
          </tr>
        )}
        {billing && (
          <>
            <tr>
              <td colSpan={2}>{t('pages.clients.policy.billed')}</td>
              <td colSpan={2}>{exactBytes(billing.billed, billing.remainder)}</td>
            </tr>
            <tr>
              <td colSpan={2}>{t('pages.clients.policy.currentMultiplier')}</td>
              <td colSpan={2}>{billing.multiplier}×</td>
            </tr>
          </>
        )}
      </tbody>
    </table>
  );

  const rootClass = [
    'client-traffic-cell',
    compact ? 'is-compact' : '',
    display.isUnlimited ? 'is-unlimited' : '',
  ]
    .filter(Boolean)
    .join(' ');

  return (
    <Popover content={popover} trigger={['hover', 'click']} placement="top">
      <div className={rootClass}>
        <span className="client-traffic-cell-used">{usedLabel}</span>
        <Progress
          className="client-traffic-cell-bar"
          aria-valuenow={display.percent}
          aria-label={
            billing
              ? `${t('pages.clients.policy.billed')}: ${exactBytes(billing.billed, billing.remainder)} / ${billing.unlimited ? t('subscription.unlimited') : exactBytes(billing.quota)}`
              : `${usedLabel} / ${display.isUnlimited ? t('subscription.unlimited') : quotaLabel}`
          }
          percent={display.percent}
          showInfo={false}
          strokeColor={display.strokeColor}
          status={display.status}
          size={compact ? 'small' : 'medium'}
        />
        <span className="client-traffic-cell-limit">
          {display.isUnlimited ? (
            <span
              className="client-traffic-cell-infinity"
              role="img"
              aria-label={t('subscription.unlimited')}
            >
              <InfinityIcon />
            </span>
          ) : (
            quotaLabel
          )}
        </span>
      </div>
    </Popover>
  );
});

export default ClientTrafficCell;

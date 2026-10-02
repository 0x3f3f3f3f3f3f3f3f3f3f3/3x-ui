import { memo, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Popover, Progress } from 'antd';
import { ClockCircleOutlined } from '@ant-design/icons';

import InfinityIcon from '@/components/ui/InfinityIcon';
import { useTheme } from '@/hooks/useTheme';
import { computeTrafficDisplay } from '@/lib/clients/traffic-display';
import { SizeFormatter } from '@/utils';
import type { ClientPolicyAccounting } from '@/schemas/client';
import './ClientTrafficCell.css';

export interface ClientTrafficCellProps {
  up?: number;
  down?: number;
  total?: number;
  enabled?: boolean;
  trafficDiff?: number;
  compact?: boolean;
  accounting?: ClientPolicyAccounting | null;
}

// Stable props skip the popover and progress work on quiet traffic updates.
const ClientTrafficCell = memo(function ClientTrafficCell({
  up = 0,
  down = 0,
  total = 0,
  enabled = true,
  trafficDiff = 0,
  compact = false,
  accounting,
}: ClientTrafficCellProps) {
  const { t } = useTranslation();
  const { isDark } = useTheme();

  const display = useMemo(
    () => computeTrafficDisplay({ up, down, total, enabled, trafficDiff, accounting }, isDark),
    [up, down, total, enabled, trafficDiff, accounting, isDark],
  );

  const pendingLabel = accounting?.resetPending
    ? t('pages.clients.accounting.resetPending')
    : accounting &&
        (accounting.policyPending || accounting.appliedVersion !== accounting.desiredVersion)
      ? t('pages.clients.accounting.policyPending')
      : null;
  const accountingRows = accounting
    ? [
        [t('pages.clients.accounting.periodBilled'), accounting.period.billed],
        [t('pages.clients.accounting.periodUncertain'), accounting.period.uncertain],
        [t('pages.clients.accounting.lifetimeUpload'), accounting.lifetime.upload],
        [t('pages.clients.accounting.lifetimeDownload'), accounting.lifetime.download],
        [t('pages.clients.accounting.lifetimeBilled'), accounting.lifetime.billed],
        [t('pages.clients.accounting.lifetimeUncertain'), accounting.lifetime.uncertain],
        ...(accounting.budget
          ? [
              [t('pages.clients.accounting.allocated'), accounting.budget.allocated],
              [t('pages.clients.accounting.frozen'), accounting.budget.frozen],
              ...(accounting.budget.unallocated !== null
                ? [[t('pages.clients.accounting.unallocated'), accounting.budget.unallocated]]
                : []),
            ]
          : []),
      ]
    : [];

  const popover = (
    <table className="client-traffic-popover">
      <tbody>
        <tr>
          <td>↑</td>
          <td>{SizeFormatter.sizeFormat(display.up)}</td>
          <td>↓</td>
          <td>{SizeFormatter.sizeFormat(display.down)}</td>
        </tr>
        {!display.isUnlimited && (
          <tr>
            <td colSpan={2}>{t('remained')}</td>
            <td colSpan={2} title={accounting?.remaining ? `${accounting.remaining} B` : undefined}>
              {SizeFormatter.sizeFormat(display.remaining)}
            </td>
          </tr>
        )}
        {accountingRows.map(([label, value]) => (
          <tr key={label}>
            <td colSpan={2}>{label}</td>
            <td colSpan={2} title={`${value} B`}>
              {SizeFormatter.sizeFormat(Number(value))}
            </td>
          </tr>
        ))}
        {accounting && (
          <tr>
            <td colSpan={2}>{t('pages.clients.accounting.policyVersion')}</td>
            <td colSpan={2}>
              {accounting.appliedVersion} / {accounting.desiredVersion}
            </td>
          </tr>
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
        <span className="client-traffic-cell-used">
          {pendingLabel && (
            <ClockCircleOutlined
              className="client-traffic-cell-pending"
              role="status"
              aria-label={pendingLabel}
              title={pendingLabel}
            />
          )}
          {SizeFormatter.sizeFormat(display.used)}
        </span>
        <Progress
          className="client-traffic-cell-bar"
          aria-label={`${SizeFormatter.sizeFormat(display.used)} / ${display.isUnlimited ? t('subscription.unlimited') : SizeFormatter.sizeFormat(display.total)}`}
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
            SizeFormatter.sizeFormat(display.total)
          )}
        </span>
      </div>
    </Popover>
  );
});

export default ClientTrafficCell;

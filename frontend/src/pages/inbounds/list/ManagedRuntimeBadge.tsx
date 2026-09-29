import { Tag, Tooltip } from 'antd';
import { useTranslation } from 'react-i18next';

import type { SSHRuntimeStatus, MieruRuntimeStatus } from '@/generated/zod';

const states: Record<string, { key: string; color: string }> = {
  disabled: { key: 'pages.inbounds.managedRuntime.disabled', color: 'default' },
  idle: { key: 'pages.inbounds.managedRuntime.idle', color: 'default' },
  pending: { key: 'pages.inbounds.managedRuntime.pending', color: 'processing' },
  running: { key: 'pages.inbounds.managedRuntime.running', color: 'success' },
  protected: { key: 'pages.inbounds.managedRuntime.protected', color: 'error' },
  unsupported: { key: 'pages.inbounds.managedRuntime.unsupported', color: 'warning' },
};

const reasons: Record<string, string> = {
  'awaiting configuration': 'pages.inbounds.managedRuntime.awaitingConfiguration',
  'remote mieru runtime unavailable': 'pages.inbounds.managedRuntime.remoteUnavailable',
  'remote SSH runtime unavailable': 'pages.inbounds.managedRuntime.remoteUnavailable',
  'awaiting disable': 'pages.inbounds.managedRuntime.awaitingDisable',
  'no enabled clients': 'pages.inbounds.managedRuntime.noClients',
  'awaiting client update': 'pages.inbounds.managedRuntime.awaitingClients',
  'waiting for the applied Xray configuration': 'pages.inbounds.managedRuntime.waitingForRouter',
  'client rate policy unavailable': 'pages.inbounds.managedRuntime.ratePolicyUnavailable',
  'client accounting policy unavailable': 'pages.inbounds.managedRuntime.accountingUnavailable',
  'listener stopped': 'pages.inbounds.managedRuntime.listenerStopped',
  'client credentials could not be applied': 'pages.inbounds.managedRuntime.credentialsUnavailable',
  'authenticated routing bridge unavailable': 'pages.inbounds.managedRuntime.routingUnavailable',
  'invalid server configuration': 'pages.inbounds.managedRuntime.invalidConfiguration',
  'listener unavailable': 'pages.inbounds.managedRuntime.listenerUnavailable',
};

function ManagedRuntimeBadge({
  status,
  protocol,
  sessions,
}: {
  status?: { state: string; reason: string; count: number };
  protocol: 'SSH' | 'mieru';
  sessions: boolean;
}) {
  const { t } = useTranslation();
  const state = status && Object.hasOwn(states, status.state) ? states[status.state] : undefined;
  const label = t(state?.key ?? 'pages.inbounds.managedRuntime.unavailable');
  const count = state && status?.state !== 'unsupported' ? status?.count : undefined;
  const reason =
    status?.reason && Object.hasOwn(reasons, status.reason) ? reasons[status.reason] : undefined;
  return (
    <Tooltip
      trigger={['hover', 'focus']}
      title={
        <div>
          <div>
            {t(
              sessions
                ? 'pages.inbounds.managedRuntime.mieruExplanation'
                : 'pages.inbounds.managedRuntime.explanation',
              { protocol },
            )}
          </div>
          {count !== undefined && (
            <div>
              {t(
                sessions
                  ? 'pages.inbounds.managedRuntime.sessions'
                  : 'pages.inbounds.managedRuntime.connections',
                { count, protocol },
              )}
            </div>
          )}
          {state && status?.reason && (
            <div>{t(reason ?? 'pages.inbounds.managedRuntime.unknownReason', { protocol })}</div>
          )}
        </div>
      }
    >
      <Tag
        className="managed-runtime-badge"
        color={state?.color ?? 'default'}
        role="status"
        aria-label={t('pages.inbounds.managedRuntime.label', { state: label, protocol })}
        tabIndex={0}
      >
        {label}
        {count !== undefined ? ` · ${count}` : ''}
      </Tag>
    </Tooltip>
  );
}

export function SSHRuntimeBadge({ status }: { status?: SSHRuntimeStatus }) {
  return (
    <ManagedRuntimeBadge
      protocol="SSH"
      sessions={false}
      status={status && { ...status, count: status.authenticatedConnections }}
    />
  );
}

export function MieruRuntimeBadge({ status }: { status?: MieruRuntimeStatus }) {
  return (
    <ManagedRuntimeBadge
      protocol="mieru"
      sessions
      status={status && { ...status, count: status.authenticatedSessions }}
    />
  );
}

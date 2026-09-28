import { Tag, Tooltip } from 'antd';
import { useTranslation } from 'react-i18next';

import type { SSHRuntimeStatus } from '@/generated/zod';

const states: Record<string, { key: string; color: string }> = {
  disabled: { key: 'pages.inbounds.sshRuntime.disabled', color: 'default' },
  idle: { key: 'pages.inbounds.sshRuntime.idle', color: 'default' },
  pending: { key: 'pages.inbounds.sshRuntime.pending', color: 'processing' },
  running: { key: 'pages.inbounds.sshRuntime.running', color: 'success' },
  protected: { key: 'pages.inbounds.sshRuntime.protected', color: 'error' },
  unsupported: { key: 'pages.inbounds.sshRuntime.unsupported', color: 'warning' },
};

const reasons: Record<string, string> = {
  'awaiting configuration': 'pages.inbounds.sshRuntime.awaitingConfiguration',
  'remote SSH runtime unavailable': 'pages.inbounds.sshRuntime.remoteUnavailable',
  'awaiting disable': 'pages.inbounds.sshRuntime.awaitingDisable',
  'no enabled clients': 'pages.inbounds.sshRuntime.noClients',
  'awaiting client update': 'pages.inbounds.sshRuntime.awaitingClients',
  'waiting for the applied Xray configuration': 'pages.inbounds.sshRuntime.waitingForRouter',
  'client rate policy unavailable': 'pages.inbounds.sshRuntime.ratePolicyUnavailable',
  'client accounting policy unavailable': 'pages.inbounds.sshRuntime.accountingUnavailable',
  'listener stopped': 'pages.inbounds.sshRuntime.listenerStopped',
  'client credentials could not be applied': 'pages.inbounds.sshRuntime.credentialsUnavailable',
  'authenticated routing bridge unavailable': 'pages.inbounds.sshRuntime.routingUnavailable',
  'invalid server configuration': 'pages.inbounds.sshRuntime.invalidConfiguration',
  'listener unavailable': 'pages.inbounds.sshRuntime.listenerUnavailable',
};

export function SSHRuntimeBadge({ status }: { status?: SSHRuntimeStatus }) {
  const { t } = useTranslation();
  const state = status && Object.hasOwn(states, status.state) ? states[status.state] : undefined;
  const label = t(state?.key ?? 'pages.inbounds.sshRuntime.unavailable');
  const count =
    state && status?.state !== 'unsupported' ? status?.authenticatedConnections : undefined;
  const reason =
    status?.reason && Object.hasOwn(reasons, status.reason) ? reasons[status.reason] : undefined;
  return (
    <Tooltip
      trigger={['hover', 'focus']}
      title={
        <div>
          <div>{t('pages.inbounds.sshRuntime.explanation')}</div>
          {count !== undefined && (
            <div>{t('pages.inbounds.sshRuntime.connections', { count })}</div>
          )}
          {state && status?.reason && (
            <div>{t(reason ?? 'pages.inbounds.sshRuntime.unknownReason')}</div>
          )}
        </div>
      }
    >
      <Tag
        className="ssh-runtime-badge"
        color={state?.color ?? 'default'}
        role="status"
        aria-label={t('pages.inbounds.sshRuntime.label', { state: label })}
        tabIndex={0}
      >
        {label}
        {count !== undefined ? ` · ${count}` : ''}
      </Tag>
    </Tooltip>
  );
}

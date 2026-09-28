import { Alert, Space, Typography } from 'antd';
import { useTranslation } from 'react-i18next';
import type { ClientRecord, InboundOption } from '@/schemas/client';
import { QrPanel } from '@/pages/inbounds/qr';
import { buildSSHClientExport } from './sshConfig';

export default function SSHConfigExport({
  client,
  inbound,
  publicHost = '',
}: {
  client: ClientRecord;
  inbound: InboundOption;
  publicHost?: string;
}) {
  const { t } = useTranslation();
  const bundle = buildSSHClientExport(client, inbound, window.location.hostname, publicHost);
  if (!bundle)
    return <Alert type="error" showIcon title={t('pages.clients.ssh.exportUnavailable')} />;
  return (
    <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
      <Alert type="info" showIcon title={t('pages.clients.ssh.exportHelp')} />
      <QrPanel
        showQr={false}
        value={bundle.config}
        remark={bundle.configName}
        downloadName={bundle.configName}
      />
      <QrPanel
        showQr={false}
        value={bundle.knownHosts}
        remark={bundle.knownHostsName}
        downloadName={bundle.knownHostsName}
      />
      <Typography.Paragraph>{t('pages.clients.ssh.runHelp')}</Typography.Paragraph>
      <Typography.Paragraph code copyable>
        {`ssh -F ./${bundle.configName} -i /path/to/client-private-key -D 127.0.0.1:1080 ${bundle.alias}`}
      </Typography.Paragraph>
    </Space>
  );
}

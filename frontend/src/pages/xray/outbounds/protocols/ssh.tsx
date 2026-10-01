import { useTranslation } from 'react-i18next';
import { Input, InputNumber, Typography } from 'antd';
import { FormField, rhfZodValidate } from '@/components/form/rhf';
import { SSHOutboundSettingsSchema } from '@/schemas/protocols/outbound/ssh';

export default function SSHFields() {
  const { t } = useTranslation();
  const fields = SSHOutboundSettingsSchema.shape;
  return (
    <>
      <FormField
        name={['settings', 'username']}
        label={t('pages.clients.sshUsername')}
        rules={{ validate: rhfZodValidate(fields.username) }}
      >
        <Input />
      </FormField>
      <FormField
        name={['settings', 'password']}
        label={t('pages.clients.sshPassword')}
        rules={{ validate: rhfZodValidate(fields.password) }}
      >
        <Input.Password />
      </FormField>
      <FormField
        name={['settings', 'privateKeyFile']}
        label={t('pages.xray.outboundForm.sshPrivateKeyFile')}
        rules={{ validate: rhfZodValidate(fields.privateKeyFile) }}
      >
        <Input placeholder="/var/lib/x-ui/native-ssh/outbound/account.pem" />
      </FormField>
      <Typography.Paragraph type="secondary">
        {t('pages.xray.outboundForm.sshPrivateKeyDescription')}
      </Typography.Paragraph>
      <FormField
        name={['settings', 'hostKey']}
        label={t('pages.xray.outboundForm.sshHostKey')}
        rules={{ validate: rhfZodValidate(fields.hostKey) }}
      >
        <Input.TextArea rows={2} />
      </FormField>
      <FormField
        name={['settings', 'handshakeTimeoutSeconds']}
        label={t('pages.inbounds.form.ssh.handshakeTimeout')}
        rules={{ validate: rhfZodValidate(fields.handshakeTimeoutSeconds) }}
      >
        <InputNumber min={0} max={120} precision={0} />
      </FormField>
      <FormField
        name={['settings', 'idleTimeoutSeconds']}
        label={t('pages.inbounds.form.ssh.idleTimeout')}
        rules={{ validate: rhfZodValidate(fields.idleTimeoutSeconds) }}
      >
        <InputNumber min={0} max={86400} precision={0} />
      </FormField>
    </>
  );
}

import { useTranslation } from 'react-i18next';
import { useFormContext, useWatch } from 'react-hook-form';
import { InputNumber, Select, Switch, Typography } from 'antd';
import { FormField, rhfZodValidate } from '@/components/form/rhf';
import { SSHInboundSettingsSchema, SSHReverseFieldsSchema } from '@/schemas/protocols/inbound/ssh';
import type { InboundFormValues } from '@/schemas/forms/inbound-form';

const limits = [
  ['handshakeTimeoutSeconds', 'handshakeTimeout', 120],
  ['channelOpenTimeoutSeconds', 'channelOpenTimeout', 120],
  ['idleTimeoutSeconds', 'idleTimeout', 86400],
  ['maxAuthTries', 'maxAuthTries', 16],
  ['maxConnections', 'maxConnections', 1024],
  ['maxConnectionsPerUser', 'maxConnectionsPerUser', 64],
  ['maxChannelsPerConnection', 'maxChannelsPerConnection', 64],
  ['maxChannels', 'maxChannels', 512],
] as const;

export default function SSHFields() {
  const { t } = useTranslation();
  const { control } = useFormContext<InboundFormValues>();
  const reverseEnabled = useWatch({ control, name: 'settings.reverse.enabled' });
  const fields = SSHInboundSettingsSchema.shape;
  const reverse = SSHReverseFieldsSchema.shape;
  return (
    <>
      <Typography.Paragraph type="secondary">
        {t('pages.inbounds.form.ssh.description')}
      </Typography.Paragraph>
      <FormField
        name={['settings', 'allowPassword']}
        label={t('pages.inbounds.form.ssh.allowPassword')}
        valueProp="checked"
      >
        <Switch />
      </FormField>
      {limits.map(([name, label, max]) => (
        <FormField
          key={name}
          name={['settings', name]}
          label={t(`pages.inbounds.form.ssh.${label}`)}
          rules={{ validate: rhfZodValidate(fields[name]) }}
        >
          <InputNumber min={0} max={max} precision={0} />
        </FormField>
      ))}
      <FormField
        name={['settings', 'reverse', 'enabled']}
        label={t('pages.inbounds.form.ssh.reverseEnabled')}
        valueProp="checked"
      >
        <Switch />
      </FormField>
      {reverseEnabled && (
        <>
          <Typography.Paragraph type="secondary">
            {t('pages.inbounds.form.ssh.reverseDescription')}
          </Typography.Paragraph>
          <FormField
            name={['settings', 'reverse', 'bindAddresses']}
            label={t('pages.inbounds.form.ssh.reverseBindAddresses')}
            rules={{ validate: rhfZodValidate(reverse.bindAddresses) }}
          >
            <Select mode="tags" tokenSeparators={[',', ' ']} />
          </FormField>
          <FormField
            name={['settings', 'reverse', 'portFrom']}
            label={t('pages.inbounds.form.ssh.reversePortFrom')}
            rules={{ validate: rhfZodValidate(reverse.portFrom) }}
          >
            <InputNumber min={1} max={65535} precision={0} />
          </FormField>
          <FormField
            name={['settings', 'reverse', 'portTo']}
            label={t('pages.inbounds.form.ssh.reversePortTo')}
            rules={{ validate: rhfZodValidate(reverse.portTo) }}
          >
            <InputNumber min={1} max={65535} precision={0} />
          </FormField>
          <FormField
            name={['settings', 'reverse', 'sourceCIDRs']}
            label={t('pages.inbounds.form.ssh.reverseSourceCIDRs')}
            rules={{ validate: rhfZodValidate(reverse.sourceCIDRs) }}
          >
            <Select mode="tags" tokenSeparators={[',', ' ']} />
          </FormField>
          <FormField
            name={['settings', 'reverse', 'maxListeners']}
            label={t('pages.inbounds.form.ssh.reverseMaxListeners')}
            rules={{ validate: rhfZodValidate(reverse.maxListeners) }}
          >
            <InputNumber min={0} max={16} precision={0} />
          </FormField>
          <FormField
            name={['settings', 'reverse', 'allowPortZero']}
            label={t('pages.inbounds.form.ssh.reverseAllowPortZero')}
            valueProp="checked"
          >
            <Switch />
          </FormField>
        </>
      )}
    </>
  );
}

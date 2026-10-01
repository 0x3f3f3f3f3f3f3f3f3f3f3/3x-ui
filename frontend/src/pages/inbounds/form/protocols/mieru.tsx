import { useTranslation } from 'react-i18next';
import { InputNumber, Select, Switch } from 'antd';
import { FormField, rhfZodValidate } from '@/components/form/rhf';
import { MieruInboundSettingsSchema } from '@/schemas/protocols/inbound/mieru';

export default function MieruFields() {
  const { t } = useTranslation();
  const fields = MieruInboundSettingsSchema.shape;
  return (
    <>
      <FormField
        name={['settings', 'transport']}
        label={t('pages.inbounds.form.mieru.transport')}
        rules={{ validate: rhfZodValidate(fields.transport) }}
      >
        <Select options={['TCP', 'UDP'].map((value) => ({ value, label: value }))} />
      </FormField>
      <FormField
        name={['settings', 'mtu']}
        label="MTU"
        rules={{ validate: rhfZodValidate(fields.mtu) }}
      >
        <InputNumber min={0} max={1500} precision={0} />
      </FormField>
      <FormField
        name={['settings', 'maxConnections']}
        label={t('pages.inbounds.form.mieru.maxConnections')}
        rules={{ validate: rhfZodValidate(fields.maxConnections) }}
      >
        <InputNumber min={0} max={0xffffffff} precision={0} />
      </FormField>
      <FormField
        name={['settings', 'handshakeTimeoutSeconds']}
        label={t('pages.inbounds.form.mieru.handshakeTimeout')}
        rules={{ validate: rhfZodValidate(fields.handshakeTimeoutSeconds) }}
      >
        <InputNumber min={0} max={0xffffffff} precision={0} />
      </FormField>
      <FormField
        name={['settings', 'userHintRequired']}
        label={t('pages.inbounds.form.mieru.userHint')}
        valueProp="checked"
      >
        <Switch />
      </FormField>
    </>
  );
}

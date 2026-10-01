import { useTranslation } from 'react-i18next';
import { Input, InputNumber, Select } from 'antd';
import { FormField, rhfZodValidate } from '@/components/form/rhf';
import { MieruOutboundSettingsSchema } from '@/schemas/protocols/outbound/mieru';
import { MieruMultiplexingSchema } from '@/schemas/protocols/shared/mieru';

export default function MieruFields() {
  const { t } = useTranslation();
  const fields = MieruOutboundSettingsSchema.shape;
  return (
    <>
      <FormField
        name={['settings', 'username']}
        label={t('pages.clients.mieruUsername')}
        rules={{ validate: rhfZodValidate(fields.username) }}
      >
        <Input />
      </FormField>
      <FormField
        name={['settings', 'password']}
        label={t('pages.clients.mieruPassword')}
        rules={{ validate: rhfZodValidate(fields.password) }}
      >
        <Input.Password />
      </FormField>
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
        name={['settings', 'multiplexing']}
        label={t('pages.inbounds.form.mieru.multiplexing')}
        rules={{ validate: rhfZodValidate(fields.multiplexing) }}
      >
        <Select
          options={MieruMultiplexingSchema.options.map((value) => ({
            value,
            label: value.replace('MULTIPLEXING_', ''),
          }))}
        />
      </FormField>
    </>
  );
}

import { useTranslation } from 'react-i18next';
import { useFormContext, useWatch } from 'react-hook-form';
import { Input, Switch } from 'antd';
import { FormField, rhfZodValidate } from '@/components/form/rhf';
import SnellOptionsFields from '@/lib/xray/forms/SnellOptionsFields';
import { SnellOutboundSettingsSchema } from '@/schemas/protocols/outbound/snell';

export default function SnellFields() {
  const { t } = useTranslation();
  const { control } = useFormContext();
  const obfs = useWatch({ control, name: 'settings.obfs' });
  return (
    <>
      <SnellOptionsFields outbound />
      <FormField
        name={['settings', 'psk']}
        label={t('pages.clients.snellPsk')}
        required
        rules={{ validate: rhfZodValidate(SnellOutboundSettingsSchema.shape.psk) }}
      >
        <Input.Password />
      </FormField>
      {obfs === 'http' && (
        <>
          <FormField
            name={['settings', 'obfsHost']}
            label={t('pages.inbounds.form.snell.obfsHost')}
          >
            <Input />
          </FormField>
          <FormField name={['settings', 'obfsUri']} label={t('pages.inbounds.form.snell.obfsUri')}>
            <Input />
          </FormField>
        </>
      )}
      <FormField
        name={['settings', 'reuse']}
        label={t('pages.inbounds.form.snell.reuse')}
        valueProp="checked"
      >
        <Switch />
      </FormField>
    </>
  );
}

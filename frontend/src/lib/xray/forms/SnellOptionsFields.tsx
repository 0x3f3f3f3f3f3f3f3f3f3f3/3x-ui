import { useTranslation } from 'react-i18next';
import { useFormContext, useWatch } from 'react-hook-form';
import { Select, Switch } from 'antd';
import { FormField } from '@/components/form/rhf';

export default function SnellOptionsFields({ outbound = false }: { outbound?: boolean }) {
  const { t } = useTranslation();
  const { control, setValue, getValues } = useFormContext();
  const version = useWatch({ control, name: 'settings.version' });
  function clearOutboundObfs() {
    if (typeof getValues('settings.psk') === 'string') {
      setValue('settings.obfsHost', '');
      setValue('settings.obfsUri', '');
    }
  }
  return (
    <>
      <FormField
        name={['settings', 'version']}
        label={t('pages.inbounds.form.snell.version')}
        onAfterChange={(value) => {
          setValue('settings.mode', '');
          if (value !== 5) setValue('settings.quic', false);
          if (value === 6) {
            setValue('settings.obfs', 'off');
            clearOutboundObfs();
          }
        }}
      >
        <Select options={[4, 5, 6].map((value) => ({ value, label: `v${value}` }))} />
      </FormField>
      {version !== 6 && (
        <FormField
          name={['settings', 'obfs']}
          label={t('pages.inbounds.form.snell.obfs')}
          onAfterChange={(value) => {
            if (value !== 'http') clearOutboundObfs();
          }}
        >
          <Select options={['off', 'http'].map((value) => ({ value, label: value }))} />
        </FormField>
      )}
      {version === 6 && (
        <FormField name={['settings', 'mode']} label={t('pages.inbounds.form.snell.mode')}>
          <Select
            options={['', 'default', 'unshaped'].map((value) => ({
              value,
              label: value || 'default',
            }))}
          />
        </FormField>
      )}
      {outbound && version === 5 && (
        <FormField
          name={['settings', 'quic']}
          label={t('pages.inbounds.form.snell.quic')}
          valueProp="checked"
          extra={t('pages.inbounds.form.snell.quicHelp')}
        >
          <Switch />
        </FormField>
      )}
    </>
  );
}

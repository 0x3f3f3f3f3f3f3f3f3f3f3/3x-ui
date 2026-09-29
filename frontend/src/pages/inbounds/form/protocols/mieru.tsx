import { useTranslation } from 'react-i18next';
import { Select } from 'antd';
import { FormField } from '@/components/form/rhf';

export default function MieruFields() {
  const { t } = useTranslation();
  return (
    <FormField
      name={['settings', 'network']}
      label={t('pages.inbounds.form.mieruTransport')}
      extra={t('pages.inbounds.form.mieruTransportDesc')}
    >
      <Select
        id="mieru-network"
        options={[
          { value: 'tcp', label: 'TCP' },
          { value: 'udp', label: 'UDP' },
          { value: 'both', label: 'TCP, UDP' },
        ]}
      />
    </FormField>
  );
}

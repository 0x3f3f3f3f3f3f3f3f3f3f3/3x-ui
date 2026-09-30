import { useTranslation } from 'react-i18next';
import { Button, Form, Input, Space } from 'antd';
import { MinusOutlined, PlusOutlined } from '@ant-design/icons';
import { useFieldArray, useFormContext, useWatch } from 'react-hook-form';

import { RandomUtil } from '@/utils';
import { InputAddon } from '@/components/ui';
import { FormField } from '@/components/form/rhf';

import PasswordAccountOwner from './password-account-owner';

export default function AccountsList() {
  const { t } = useTranslation();
  const { control } = useFormContext();
  const { fields, append, remove } = useFieldArray({ control, name: 'settings.accounts' });
  const nodeId = useWatch({ control, name: 'nodeId' });
  const protocol = useWatch({ control, name: 'protocol' });
  const auth = useWatch({ control, name: 'settings.auth' });
  const ownerDisabled = nodeId != null || (protocol === 'mixed' && auth === 'noauth');
  return (
    <>
      <Form.Item label={t('pages.inbounds.form.accounts')}>
        <Button
          size="small"
          onClick={() =>
            append({
              user: RandomUtil.randomLowerAndNum(8),
              pass: RandomUtil.randomLowerAndNum(12),
            })
          }
        >
          <PlusOutlined /> {t('add')}
        </Button>
      </Form.Item>
      {fields.length > 0 && (
        <Form.Item wrapperCol={{ span: 24 }}>
          {fields.map((field, idx) => (
            <div key={field.id}>
              <Space.Compact className="mb-8" block>
                <InputAddon>{String(idx + 1)}</InputAddon>
                <FormField name={['settings', 'accounts', idx, 'user']} noStyle>
                  <Input placeholder={t('username')} />
                </FormField>
                <FormField name={['settings', 'accounts', idx, 'pass']} noStyle>
                  <Input placeholder={t('password')} />
                </FormField>
                <Button aria-label={t('remove')} onClick={() => remove(idx)}>
                  <MinusOutlined />
                </Button>
              </Space.Compact>
              <PasswordAccountOwner index={idx} disabled={ownerDisabled} />
            </div>
          ))}
        </Form.Item>
      )}
    </>
  );
}

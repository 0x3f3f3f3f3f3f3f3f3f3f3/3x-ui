import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, Button, Form, Input, InputNumber, Space, Typography } from 'antd';
import { Controller, useFormContext } from 'react-hook-form';

import { FormField, rhfZodValidate } from '@/components/form/rhf';
import type { OutboundFormValues } from '@/schemas/forms/outbound-form';
import { SSHOutboundSettingsSchema } from '@/schemas/protocols/outbound/ssh';

export default function SSHFields() {
  const { t } = useTranslation();
  const { control } = useFormContext<OutboundFormValues>();
  const [showKey, setShowKey] = useState(false);
  const keyId = useId();
  const shape = SSHOutboundSettingsSchema.shape;
  return (
    <>
      <Alert
        type="info"
        showIcon
        title={t('pages.xray.sshOutbound.capabilityHint')}
        style={{ marginBottom: 16 }}
      />
      <FormField
        name={['settings', 'address']}
        label={t('pages.inbounds.address')}
        required
        rules={{ validate: rhfZodValidate(shape.address) }}
      >
        <Input autoComplete="off" />
      </FormField>
      <FormField
        name={['settings', 'port']}
        label={t('pages.inbounds.port')}
        required
        rules={{ validate: rhfZodValidate(shape.port) }}
      >
        <InputNumber min={1} max={65535} style={{ width: '100%' }} />
      </FormField>
      <FormField
        name={['settings', 'user']}
        label={t('username')}
        required
        rules={{ validate: rhfZodValidate(shape.user) }}
      >
        <Input autoComplete="off" />
      </FormField>
      <Controller
        control={control}
        name="settings.privateKey"
        rules={{ validate: rhfZodValidate(shape.privateKey) }}
        render={({ field, fieldState }) => (
          <Form.Item
            label={t('pages.inbounds.privatekey')}
            htmlFor={keyId}
            required
            validateStatus={fieldState.error ? 'error' : undefined}
            help={fieldState.error?.message ? t(fieldState.error.message) : undefined}
            extra={t('pages.xray.sshOutbound.keyHint')}
          >
            <Space orientation="vertical" style={{ width: '100%' }}>
              {!showKey && (
                <Typography.Text type="secondary">
                  {t(
                    field.value
                      ? 'pages.xray.sshOutbound.keyHidden'
                      : 'pages.xray.sshOutbound.keyMissing',
                  )}
                </Typography.Text>
              )}
              <Button
                id={showKey ? undefined : keyId}
                ref={showKey ? undefined : field.ref}
                aria-label={t(
                  showKey ? 'pages.xray.sshOutbound.hideKey' : 'pages.xray.sshOutbound.showKey',
                )}
                aria-expanded={showKey}
                onClick={() => setShowKey(!showKey)}
              >
                {t(showKey ? 'pages.xray.sshOutbound.hideKey' : 'pages.xray.sshOutbound.showKey')}
              </Button>
              {showKey && (
                <Input.TextArea
                  id={keyId}
                  ref={field.ref}
                  value={field.value}
                  onChange={field.onChange}
                  onBlur={field.onBlur}
                  autoSize={{ minRows: 4, maxRows: 10 }}
                  autoComplete="off"
                  spellCheck={false}
                />
              )}
            </Space>
          </Form.Item>
        )}
      />
      <FormField
        name={['settings', 'privateKeyPassphrase']}
        label={t('pages.xray.sshOutbound.passphrase')}
        rules={{ validate: rhfZodValidate(shape.privateKeyPassphrase) }}
      >
        <Input.Password autoComplete="new-password" />
      </FormField>
      <FormField
        name={['settings', 'hostKey']}
        label={t('pages.xray.sshOutbound.hostKey')}
        required
        extra={t('pages.xray.sshOutbound.hostKeyHint')}
        rules={{ validate: rhfZodValidate(shape.hostKey) }}
      >
        <Input.TextArea autoSize={{ minRows: 2, maxRows: 4 }} spellCheck={false} />
      </FormField>
    </>
  );
}

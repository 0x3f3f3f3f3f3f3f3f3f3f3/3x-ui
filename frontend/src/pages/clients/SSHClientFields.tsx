import { useFieldArray, useFormContext } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { Alert, Button, Col, Divider, Input, InputNumber, Row, Space } from 'antd';
import { DeleteOutlined, PlusOutlined } from '@ant-design/icons';
import { FormField } from '@/components/form/rhf';
import type { ClientFormValues } from '@/schemas/client';

export default function SSHClientFields() {
  const { t } = useTranslation();
  const { control } = useFormContext<ClientFormValues>();
  const targets = useFieldArray({ control, name: 'ssh.targets' });
  const reverse = useFieldArray({ control, name: 'ssh.reverse' });

  return (
    <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
      <Alert type="info" showIcon title={t('pages.clients.ssh.identityHelp')} />
      <FormField
        name="ssh.publicKeys"
        label={t('pages.clients.ssh.publicKeys')}
        required
        extra={t('pages.clients.ssh.publicKeysHelp')}
        transform={{
          input: (v) => (v as string[] | undefined)?.join('\n') ?? '',
          output: (v) => String(v).split(/\r?\n/),
        }}
      >
        <Input.TextArea rows={4} spellCheck={false} placeholder="ssh-ed25519 AAAA… laptop" />
      </FormField>
      <Divider titlePlacement="start">{t('pages.clients.ssh.targets')}</Divider>
      <Alert type="info" showIcon title={t('pages.clients.ssh.targetsHelp')} />
      {targets.fields.map((field, index) => (
        <Row key={field.id} gutter={12} align="middle">
          <Col flex="auto">
            <FormField name={`ssh.targets.${index}.host`} label={t('pages.clients.ssh.targetHost')}>
              <Input placeholder="example.com" />
            </FormField>
          </Col>
          <Col flex="120px">
            <FormField name={`ssh.targets.${index}.port`} label={t('pages.clients.ssh.targetPort')}>
              <InputNumber min={0} max={65535} style={{ width: '100%' }} />
            </FormField>
          </Col>
          <Col>
            <Button
              danger
              icon={<DeleteOutlined />}
              aria-label={t('pages.clients.ssh.removeTarget')}
              onClick={() => targets.remove(index)}
            />
          </Col>
        </Row>
      ))}
      <Button
        icon={<PlusOutlined />}
        disabled={targets.fields.length >= 256}
        onClick={() => targets.append({ host: '', port: 443 })}
      >
        {t('pages.clients.ssh.addTarget')}
      </Button>
      <Divider titlePlacement="start">{t('pages.clients.ssh.reverse')}</Divider>
      <Alert type="warning" showIcon title={t('pages.clients.ssh.reverseHelp')} />
      {reverse.fields.map((field, index) => (
        <Row key={field.id} gutter={12} align="middle">
          <Col flex="auto">
            <FormField
              name={`ssh.reverse.${index}.address`}
              label={t('pages.clients.ssh.reverseAddress')}
            >
              <Input placeholder="127.0.0.1" />
            </FormField>
          </Col>
          <Col flex="120px">
            <FormField
              name={`ssh.reverse.${index}.port`}
              label={t('pages.clients.ssh.reversePort')}
            >
              <InputNumber min={0} max={65535} style={{ width: '100%' }} />
            </FormField>
          </Col>
          <Col>
            <Button
              danger
              icon={<DeleteOutlined />}
              aria-label={t('pages.clients.ssh.removeReverse')}
              onClick={() => reverse.remove(index)}
            />
          </Col>
        </Row>
      ))}
      <Button
        icon={<PlusOutlined />}
        disabled={reverse.fields.length >= 16}
        onClick={() => reverse.append({ address: '127.0.0.1', port: 0 })}
      >
        {t('pages.clients.ssh.addReverse')}
      </Button>
    </Space>
  );
}

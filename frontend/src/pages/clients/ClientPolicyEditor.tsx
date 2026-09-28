import { useState } from 'react';
import {
  Alert,
  Button,
  Col,
  Descriptions,
  Form,
  Input,
  InputNumber,
  Row,
  Space,
  Spin,
  Typography,
} from 'antd';
import { FormProvider } from 'react-hook-form';
import { useTranslation } from 'react-i18next';

import { FormField, useZodForm } from '@/components/form/rhf';
import { useClientPolicy } from '@/hooks/useClients';
import {
  ClientPolicyUpdateSchema,
  type ClientPolicy,
  type ClientPolicyUpdate,
} from '@/schemas/client-policy';
import { exactBytes } from '@/lib/traffic/exact-bytes';

type PolicyState = ReturnType<typeof useClientPolicy>;

function PolicyForm({ query, mutation, initial }: PolicyState & { initial: ClientPolicy }) {
  const { t } = useTranslation();
  const [loaded, setLoaded] = useState(initial);
  const [mustReload, setMustReload] = useState(false);
  const [failure, setFailure] = useState('');
  const [applied, setApplied] = useState(false);
  const [reloading, setReloading] = useState(false);
  const methods = useZodForm<ClientPolicyUpdate>(ClientPolicyUpdateSchema, {
    defaultValues: { ...initial, scope: 'local' },
  });
  const current = query.data ?? loaded;
  const stale = current.policyId !== loaded.policyId || current.version !== loaded.version;
  const unsupported = !current.supported || current.scope !== 'local';
  const disabled = unsupported || mutation.isPending || reloading;
  const reloadRequired = mustReload || stale;
  const usage = current.usage;

  function accept(policy: ClientPolicy) {
    setLoaded(policy);
    methods.reset({ ...policy, scope: 'local' });
    setMustReload(false);
    setFailure('');
  }

  async function reload() {
    setReloading(true);
    setApplied(false);
    const result = await query.refetch();
    if (result.isSuccess) accept(result.data);
    setReloading(false);
  }

  const apply = methods.handleSubmit(async (values) => {
    if (disabled || reloadRequired || query.isError) return;
    setApplied(false);
    try {
      accept(await mutation.mutateAsync(values));
      setApplied(true);
    } catch (error) {
      setFailure(error instanceof Error ? error.message : String(error));
      setMustReload(true);
    }
  });

  return (
    <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
      <Typography.Paragraph>{t('pages.clients.policy.localScope')}</Typography.Paragraph>
      {unsupported && (
        <Alert type="warning" showIcon title={t('pages.clients.policy.unsupported')} />
      )}
      {reloadRequired && (
        <Alert type="warning" showIcon title={t('pages.clients.policy.reloadRequired')} />
      )}
      {failure && <Alert type="error" showIcon title={failure} />}
      {applied && !reloadRequired && !methods.formState.isDirty && (
        <Alert type="success" showIcon title={t('pages.clients.policy.applied')} />
      )}
      <Descriptions
        column={{ xs: 1, sm: 2 }}
        size="small"
        bordered
        items={[
          { key: 'up', label: t('pages.clients.policy.rawUp'), children: exactBytes(usage.up) },
          {
            key: 'down',
            label: t('pages.clients.policy.rawDown'),
            children: exactBytes(usage.down),
          },
          {
            key: 'billed',
            label: t('pages.clients.policy.billed'),
            children: exactBytes(usage.billed, usage.remainder),
          },
          {
            key: 'quota',
            label: t('pages.clients.policy.quota'),
            children: usage.unlimited ? t('unlimited') : exactBytes(usage.quota),
          },
          {
            key: 'remaining',
            label: t('pages.clients.policy.remaining'),
            children: usage.unlimited
              ? t('unlimited')
              : exactBytes(usage.remaining, -usage.remainder),
          },
          {
            key: 'multiplier',
            label: t('pages.clients.policy.currentMultiplier'),
            children: `${current.multiplier}×`,
          },
        ]}
      />
      <Typography.Text type="secondary">{t('pages.clients.policy.byteUnits')}</Typography.Text>
      <FormProvider {...methods}>
        <Form component="div" layout="vertical" disabled={disabled}>
          <Row gutter={16}>
            <Col xs={24} sm={12}>
              <FormField name="uploadBps" label={t('pages.clients.policy.upload')}>
                <InputNumber changeOnBlur={false} style={{ width: '100%' }} />
              </FormField>
            </Col>
            <Col xs={24} sm={12}>
              <FormField name="downloadBps" label={t('pages.clients.policy.download')}>
                <InputNumber changeOnBlur={false} style={{ width: '100%' }} />
              </FormField>
            </Col>
          </Row>
          <Typography.Paragraph type="secondary">
            {t('pages.clients.policy.rateUnits')}
          </Typography.Paragraph>
          <FormField
            name="multiplier"
            label={t('pages.clients.policy.multiplier')}
            extra={t('pages.clients.policy.multiplierHelp')}
          >
            <Input inputMode="decimal" />
          </FormField>
        </Form>
      </FormProvider>
      <Typography.Paragraph type="secondary">
        {t('pages.clients.policy.applyHelp')}
      </Typography.Paragraph>
      <Space wrap>
        <Button
          type="primary"
          loading={mutation.isPending}
          disabled={disabled || reloadRequired || query.isError || !methods.formState.isDirty}
          onClick={() => void apply()}
        >
          {t('pages.clients.policy.apply')}
        </Button>
        <Button loading={reloading} disabled={mutation.isPending} onClick={() => void reload()}>
          {t('pages.clients.policy.reload')}
        </Button>
      </Space>
    </Space>
  );
}

export default function ClientPolicyEditor({ email, active }: { email: string; active: boolean }) {
  const { t } = useTranslation();
  const state = useClientPolicy(email, active);
  return (
    <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
      {state.query.isError && (
        <Alert
          type="error"
          showIcon
          title={t('pages.clients.policy.loadFailed')}
          action={<Button onClick={() => void state.query.refetch()}>{t('refresh')}</Button>}
        />
      )}
      {state.query.data ? (
        <PolicyForm {...state} initial={state.query.data} />
      ) : (
        state.query.isPending && <Spin />
      )}
    </Space>
  );
}

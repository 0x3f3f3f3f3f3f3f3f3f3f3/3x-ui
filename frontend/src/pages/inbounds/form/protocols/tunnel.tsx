import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useInfiniteQuery } from '@tanstack/react-query';
import { useFormContext, useWatch } from 'react-hook-form';
import { Button, Input, InputNumber, Select, Switch } from 'antd';

import { HeaderMapEditor } from '@/components/form';
import { FormField } from '@/components/form/rhf';
import { fetchClientPage } from '@/hooks/useClients';
import type { InboundFormValues } from '@/schemas/forms/inbound-form';

export default function TunnelFields({ requireOwner = false }: { requireOwner?: boolean }) {
  const { t } = useTranslation();
  const { control } = useFormContext<InboundFormValues>();
  const nodeId = useWatch({ control, name: 'nodeId' });
  const ownerClientId = useWatch({ control, name: 'ownerClientId' });
  const clients = useWatch({ control, name: 'settings.clients' });
  const remote = nodeId != null;
  const [search, setSearch] = useState('');
  const [query, setQuery] = useState('');
  const [selected, setSelected] = useState<{ value: string; label: string }>();
  useEffect(() => {
    const timer = setTimeout(() => setQuery(search), 300);
    return () => clearTimeout(timer);
  }, [search]);
  const owners = useInfiniteQuery({
    queryKey: ['tunnel-owner-choices', query],
    initialPageParam: 1,
    queryFn: ({ pageParam }) => fetchClientPage({ page: pageParam, pageSize: 25, search: query }),
    getNextPageParam: (page) =>
      page.page * page.pageSize < page.filtered ? page.page + 1 : undefined,
    enabled: !remote,
  });
  const choices = new Map<string, string>();
  if (ownerClientId) {
    const email = clients?.[0]?.email;
    choices.set(
      ownerClientId,
      selected?.value === ownerClientId
        ? selected.label
        : typeof email === 'string'
          ? email
          : ownerClientId,
    );
  }
  for (const page of owners.data?.pages ?? []) {
    for (const client of page.items) {
      if (client.clientId) choices.set(client.clientId, client.email);
    }
  }
  return (
    <>
      <FormField
        name="ownerClientId"
        label={t('pages.inbounds.form.ownerClient')}
        required={requireOwner && !remote}
        rules={{
          required: requireOwner && !remote ? 'pages.inbounds.form.ownerClientRequired' : false,
        }}
        onAfterChange={(value) => {
          if (typeof value === 'string') setSelected({ value, label: choices.get(value) ?? value });
        }}
        extra={t(
          remote
            ? 'pages.inbounds.form.ownerClientLocalOnly'
            : 'pages.inbounds.form.ownerClientHelp',
        )}
      >
        <Select
          id="ownerClientId"
          disabled={remote}
          showSearch={{ filterOption: false, onSearch: setSearch }}
          loading={owners.isFetching}
          options={[...choices].map(([value, label]) => ({ value, label }))}
          notFoundContent={
            owners.isError ? t('pages.inbounds.form.ownerClientLoadError') : undefined
          }
          popupRender={(menu) => (
            <>
              {menu}
              {owners.hasNextPage && (
                <Button
                  block
                  type="text"
                  loading={owners.isFetchingNextPage}
                  onMouseDown={(event) => event.preventDefault()}
                  onClick={() => void owners.fetchNextPage()}
                >
                  {t('pages.inbounds.form.ownerClientLoadMore')}
                </Button>
              )}
            </>
          )}
        />
      </FormField>
      <FormField
        name={['settings', 'rewriteAddress']}
        label={t('pages.inbounds.form.rewriteAddress')}
      >
        <Input />
      </FormField>
      <FormField name={['settings', 'rewritePort']} label={t('pages.inbounds.form.rewritePort')}>
        <InputNumber min={0} max={65535} />
      </FormField>
      <FormField
        name={['settings', 'allowedNetwork']}
        label={t('pages.inbounds.form.allowedNetwork')}
      >
        <Select
          options={[
            { value: 'tcp,udp', label: 'TCP, UDP' },
            { value: 'tcp', label: 'TCP' },
            { value: 'udp', label: 'UDP' },
          ]}
        />
      </FormField>
      <FormField label={t('pages.inbounds.portMap')} name={['settings', 'portMap']}>
        <HeaderMapEditor mode="v1" />
      </FormField>
      <FormField
        name={['settings', 'followRedirect']}
        label={t('pages.inbounds.form.followRedirect')}
        valueProp="checked"
      >
        <Switch />
      </FormField>
    </>
  );
}

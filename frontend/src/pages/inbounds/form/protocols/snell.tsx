import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useInfiniteQuery } from '@tanstack/react-query';
import { useFormContext, useWatch } from 'react-hook-form';
import { Button, Select } from 'antd';
import { keys } from '@/api/queryKeys';
import { FormField } from '@/components/form/rhf';
import { fetchClientPage } from '@/hooks/useClients';
import SnellOptionsFields from '@/lib/xray/forms/SnellOptionsFields';
import type { InboundFormValues } from '@/schemas/forms/inbound-form';

export default function SnellFields() {
  const { t } = useTranslation();
  const { control } = useFormContext<InboundFormValues>();
  const ownerClientId = useWatch({ control, name: 'ownerClientId' });
  const clients = useWatch({ control, name: 'settings.clients' });
  const enable = useWatch({ control, name: 'enable' });
  const [search, setSearch] = useState('');
  const [query, setQuery] = useState('');
  const [selected, setSelected] = useState<{ value: string; label: string }>();
  useEffect(() => {
    const timer = setTimeout(() => setQuery(search), 300);
    return () => clearTimeout(timer);
  }, [search]);
  const owners = useInfiniteQuery({
    queryKey: keys.clients.tunnelOwners(query),
    initialPageParam: 1,
    queryFn: ({ pageParam }) => fetchClientPage({ page: pageParam, pageSize: 25, search: query }),
    getNextPageParam: (page) =>
      page.page * page.pageSize < page.filtered ? page.page + 1 : undefined,
  });
  const choices = new Map<string, string>();
  const existingEmail = typeof clients?.[0]?.email === 'string' ? clients[0].email : '';
  if (ownerClientId)
    choices.set(
      ownerClientId,
      selected?.value === ownerClientId ? selected.label : existingEmail || ownerClientId,
    );
  for (const page of owners.data?.pages ?? [])
    for (const client of page.items)
      if (client.clientId) choices.set(client.clientId, client.email);
  return (
    <>
      <SnellOptionsFields />
      <FormField
        name="ownerClientId"
        label={t('pages.inbounds.form.snell.owner')}
        required={enable && !clients?.length}
        extra={t('pages.inbounds.form.snell.ownerHelp')}
        onAfterChange={(value) => {
          if (typeof value === 'string') setSelected({ value, label: choices.get(value) ?? value });
        }}
      >
        <Select
          id="ownerClientId"
          placeholder={existingEmail || t('pages.inbounds.form.ownerClientRequired')}
          allowClear
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
    </>
  );
}

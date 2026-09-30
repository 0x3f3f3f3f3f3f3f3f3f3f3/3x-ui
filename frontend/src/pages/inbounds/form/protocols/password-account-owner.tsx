import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useInfiniteQuery } from '@tanstack/react-query';
import { useFormContext, useWatch } from 'react-hook-form';
import { Button, Select } from 'antd';

import { keys } from '@/api/queryKeys';
import { FormField } from '@/components/form/rhf';
import { fetchClientPage } from '@/hooks/useClients';
import type { InboundFormValues } from '@/schemas/forms/inbound-form';

export default function PasswordAccountOwner({
  index,
  disabled,
}: {
  index: number;
  disabled: boolean;
}) {
  const { t } = useTranslation();
  const { control } = useFormContext<InboundFormValues>();
  const accounts = useWatch({ control, name: 'settings.accounts' });
  const nodeId = useWatch({ control, name: 'nodeId' });
  const ownerClientId = accounts?.[index]?.ownerClientId;
  const [search, setSearch] = useState('');
  const [query, setQuery] = useState('');
  const [selected, setSelected] = useState<{ value: string; label: string }>();
  useEffect(() => {
    const timer = setTimeout(() => setQuery(search), 300);
    return () => clearTimeout(timer);
  }, [search]);
  const owners = useInfiniteQuery({
    queryKey: keys.clients.passwordOwners(query),
    initialPageParam: 1,
    queryFn: ({ pageParam }) => fetchClientPage({ page: pageParam, pageSize: 25, search: query }),
    getNextPageParam: (page) =>
      page.page * page.pageSize < page.filtered ? page.page + 1 : undefined,
    enabled: !disabled,
  });
  const resolvedOwner = ownerClientId
    ? owners.data?.pages
        .flatMap((page) => page.items)
        .find((client) => client.clientId === ownerClientId)
    : undefined;
  if (
    ownerClientId &&
    resolvedOwner &&
    (selected?.value !== ownerClientId || selected.label !== resolvedOwner.email)
  ) {
    setSelected({ value: ownerClientId, label: resolvedOwner.email });
  }
  const choices = new Map<string, string>();
  if (ownerClientId) {
    choices.set(ownerClientId, selected?.value === ownerClientId ? selected.label : ownerClientId);
  }
  for (const page of owners.data?.pages ?? []) {
    for (const client of page.items) {
      if (client.clientId) choices.set(client.clientId, client.email);
    }
  }
  return (
    <FormField
      name={['settings', 'accounts', index, 'ownerClientId']}
      label={t('pages.inbounds.form.ownerClient')}
      required={!disabled && Boolean(accounts?.some((account) => account.ownerClientId))}
      rules={{
        required:
          !disabled && accounts?.some((account) => account.ownerClientId)
            ? 'pages.inbounds.form.ownerClientRequired'
            : false,
      }}
      onAfterChange={(value) => {
        setSelected(
          typeof value === 'string' ? { value, label: choices.get(value) ?? value } : undefined,
        );
      }}
      extra={
        <>
          {t(
            nodeId != null
              ? 'pages.inbounds.form.passwordOwnerLocalOnly'
              : disabled
                ? 'pages.inbounds.form.passwordOwnerRequiresAuth'
                : 'pages.inbounds.form.passwordOwnerHelp',
          )}
          {owners.isError && !disabled && (
            <div role="alert">{t('pages.inbounds.form.ownerClientLoadError')}</div>
          )}
        </>
      }
    >
      <Select
        id={`passwordOwner-${index}`}
        allowClear
        disabled={disabled}
        showSearch={{ filterOption: false, onSearch: setSearch }}
        loading={owners.isFetching}
        options={[...choices].map(([value, label]) => ({ value, label }))}
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
  );
}

import { useInfiniteQuery } from '@tanstack/react-query';
import { Alert, Button, Card, Descriptions, Space, Spin, Tag, Typography } from 'antd';
import { useTranslation } from 'react-i18next';

import { ManagedPolicyAccountPageSchema } from '@/generated/zod';
import { HttpUtil } from '@/utils';

export default function ManagedPolicyAccounts({ parentClientId }: { parentClientId: string }) {
  const { t } = useTranslation();
  const query = useInfiniteQuery({
    queryKey: ['managedPolicyAccounts', parentClientId],
    initialPageParam: '',
    queryFn: async ({ pageParam }) => {
      const msg = await HttpUtil.post(
        '/panel/api/server/clientPolicyCoordinator/accounts',
        { parentClientId, afterNode: pageParam, limit: 16 },
        { silent: true },
      );
      if (!msg?.success || !msg.obj) throw new Error('Account status unavailable');
      return ManagedPolicyAccountPageSchema.parse(msg.obj);
    },
    getNextPageParam: (page) => page.nextNode || undefined,
    refetchInterval: 10_000,
  });
  if (query.isPending) return <Spin size="small" />;
  if (query.isError)
    return <Alert type="warning" title={t('pages.clients.policy.statusUnavailable')} />;
  const first = query.data.pages[0];
  return (
    <Space orientation="vertical" style={{ width: '100%' }}>
      <Typography.Text strong>
        {t(`pages.clients.policy.${first.scope === 'global' ? 'scopeGlobal' : 'scopeNode'}`)}
      </Typography.Text>
      {query.data.pages.some((page) => page.pendingEnrollment) && (
        <Tag>{t('pages.clients.policy.enrollmentPending')}</Tag>
      )}
      {query.data.pages
        .flatMap((page) => page.accounts)
        .map((account) => (
          <Card
            size="small"
            key={account.clientId}
            title={account.nodeId || t('pages.clients.policy.scopeGlobal')}
          >
            {!account.enrolled && <Tag>{t('pages.clients.policy.enrollmentPending')}</Tag>}
            {account.policyPending && <Tag>{t('pages.clients.accounting.policyPending')}</Tag>}
            {account.deleted && <Tag>{t('pages.clients.policy.accountClosed')}</Tag>}
            <Descriptions
              size="small"
              column={1}
              items={[
                {
                  key: 'billed',
                  label: t('pages.clients.accounting.periodBilled'),
                  children: `${account.windowUsed} B`,
                },
                {
                  key: 'held',
                  label: t('pages.clients.accounting.allocated'),
                  children: `${account.budget.allocated} B`,
                },
                {
                  key: 'frozen',
                  label: t('pages.clients.accounting.frozen'),
                  children: `${account.budget.frozen} B`,
                },
                {
                  key: 'remaining',
                  label: t('remained'),
                  children: account.remaining === null ? t('unlimited') : `${account.remaining} B`,
                },
                {
                  key: 'unallocated',
                  label: t('pages.clients.accounting.unallocated'),
                  children:
                    account.budget.unallocated === null
                      ? t('unlimited')
                      : `${account.budget.unallocated} B`,
                },
                {
                  key: 'version',
                  label: t('pages.clients.accounting.policyVersion'),
                  children: account.policyVersion,
                },
              ]}
            />
          </Card>
        ))}
      {query.hasNextPage && (
        <Button loading={query.isFetchingNextPage} onClick={() => void query.fetchNextPage()}>
          {t('pages.clients.policy.moreAccounts')}
        </Button>
      )}
    </Space>
  );
}

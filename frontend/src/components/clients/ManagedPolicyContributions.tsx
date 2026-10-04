import { useState } from 'react';
import { useInfiniteQuery } from '@tanstack/react-query';
import { Alert, Button, Card, Descriptions, Space, Spin, Typography } from 'antd';
import { useTranslation } from 'react-i18next';

import { ManagedPolicyContributionPageSchema } from '@/generated/zod';
import { HttpUtil } from '@/utils';

export default function ManagedPolicyContributions({
  parentClientId,
  clientId,
}: {
  parentClientId: string;
  clientId: string;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const query = useInfiniteQuery({
    queryKey: ['managedPolicyContributions', parentClientId, clientId],
    enabled: open,
    initialPageParam: '',
    queryFn: async ({ pageParam }) => {
      const msg = await HttpUtil.post(
        '/panel/api/server/clientPolicyCoordinator/contributions',
        { parentClientId, clientId, afterGrant: pageParam, limit: 16 },
        { silent: true },
      );
      if (!msg?.success || !msg.obj) throw new Error('Usage receipts unavailable');
      return ManagedPolicyContributionPageSchema.parse(msg.obj);
    },
    getNextPageParam: (page) => page.nextGrant || undefined,
    refetchInterval: 10_000,
  });
  return (
    <Space orientation="vertical" style={{ width: '100%' }}>
      <Button onClick={() => setOpen(!open)}>{t('pages.clients.policy.nodeReceipts')}</Button>
      {open && query.isPending && <Spin size="small" />}
      {open && query.isError && (
        <Alert type="warning" title={t('pages.clients.policy.statusUnavailable')} />
      )}
      {open &&
        query.data?.pages
          .flatMap((page) => page.contributions)
          .map((receipt) => (
            <Card size="small" key={receipt.grantId} title={receipt.nodeId}>
              <Descriptions
                size="small"
                column={1}
                items={[
                  {
                    key: 'upload',
                    label: t('pages.clients.accounting.lifetimeUpload'),
                    children: `${receipt.usage.upload} B`,
                  },
                  {
                    key: 'download',
                    label: t('pages.clients.accounting.lifetimeDownload'),
                    children: `${receipt.usage.download} B`,
                  },
                  {
                    key: 'billed',
                    label: t('pages.clients.accounting.lifetimeBilled'),
                    children: `${receipt.usage.billed} B`,
                  },
                  {
                    key: 'source',
                    label: t('pages.clients.policy.receiptSource'),
                    children: <Typography.Text code>{receipt.sourceId}</Typography.Text>,
                  },
                  {
                    key: 'boot',
                    label: t('pages.clients.policy.receiptBoot'),
                    children: <Typography.Text code>{receipt.bootId}</Typography.Text>,
                  },
                  {
                    key: 'grant',
                    label: t('pages.clients.policy.receiptSequence'),
                    children: (
                      <>
                        <Typography.Text code>{receipt.grantId}</Typography.Text> (
                        {receipt.grantSequence} / {receipt.reportSequence})
                      </>
                    ),
                  },
                ]}
              />
            </Card>
          ))}
      {open && query.hasNextPage && (
        <Button loading={query.isFetchingNextPage} onClick={() => void query.fetchNextPage()}>
          {t('pages.clients.policy.moreReceipts')}
        </Button>
      )}
    </Space>
  );
}

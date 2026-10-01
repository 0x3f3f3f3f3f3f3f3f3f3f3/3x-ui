import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  AutoComplete,
  Button,
  Form,
  Input,
  InputNumber,
  Modal,
  Select,
  Space,
  Switch,
  Tooltip,
  Typography,
  message,
} from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import type { Dayjs } from 'dayjs';
import { FormProvider, useForm, useWatch } from 'react-hook-form';

import { RandomUtil, SizeFormatter } from '@/utils';
import { formatInboundLabel } from '@/lib/inbounds/label';
import { TLS_FLOW_CONTROL, TRAFFIC_RESETS } from '@/schemas/primitives';
import { DateTimePicker, SelectAllClearButtons } from '@/components/form';
import { FormField } from '@/components/form/rhf';
import { useClients, type InboundOption } from '@/hooks/useClients';
import { useFail2banStatusQuery, getLimitIpNotice } from '@/api/queries/useFail2banStatusQuery';
import ClientRenewalFields from './ClientRenewalFields';
import { ClientBulkAddFormSchema, type ClientBulkAddFormValues } from '@/schemas/client';

const FLOW_OPTIONS = Object.values(TLS_FLOW_CONTROL);

const MULTI_CLIENT_PROTOCOLS = new Set([
  'shadowsocks',
  'vless',
  'vmess',
  'trojan',
  'hysteria',
  'wireguard',
  'amneziawg',
  'tuic',
  'mieru',
  'ssh',
  'snell',
]);

const EMPTY: ClientBulkAddFormValues = {
  policyUploadBytesPerSecond: null,
  policyDownloadBytesPerSecond: null,
  policyMultiplier: '',
  emailMethod: 0,
  sshAuthorizedKeys: '',
  generateSshPasswords: false,
  firstNum: 1,
  lastNum: 1,
  emailPrefix: '',
  emailPostfix: '',
  quantity: 1,
  subId: '',
  group: '',
  comment: '',
  flow: '',
  limitIp: 0,
  limitHwid: 0,
  totalGB: 0,
  expiryTime: 0,
  reset: 0,
  resetDay: 0,
  resetWeekday: 0,
  resetMax: 0,
  trafficReset: 'never' as const,
  trafficResetDay: 1,
  inboundIds: [],
};

interface ClientBulkAddModalProps {
  open: boolean;
  inbounds: InboundOption[];
  groups?: string[];
  onOpenChange: (open: boolean) => void;
  onSaved?: () => void;
}

export default function ClientBulkAddModal({
  open,
  inbounds,
  groups = [],
  onOpenChange,
  onSaved,
}: ClientBulkAddModalProps) {
  const { t } = useTranslation();
  const [messageApi, messageContextHolder] = message.useMessage();
  const { bulkCreate } = useClients({ list: false });

  const methods = useForm<ClientBulkAddFormValues>({ defaultValues: EMPTY });
  const inboundIds = useWatch({ control: methods.control, name: 'inboundIds' });
  const policyUnsupported = useMemo(() => {
    const byId = new Map(inbounds.map((inbound) => [inbound.id, inbound]));
    return (inboundIds || []).some((id) => {
      const inbound = byId.get(id);
      return !inbound || inbound.nodeId != null;
    });
  }, [inboundIds, inbounds]);
  const selectedSnell = inbounds.filter(
    (row) => row.protocol === 'snell' && (inboundIds || []).includes(row.id),
  );
  const hasSnell = selectedSnell.length > 0;
  useEffect(() => {
    if (hasSnell) {
      methods.setValue('quantity', 1);
      methods.setValue('emailMethod', 0);
    }
  }, [hasSnell, methods]);
  const selectedSSH = inbounds.filter(
    (row) => row.protocol === 'ssh' && (inboundIds || []).includes(row.id),
  );
  const allSSHAllowPassword =
    selectedSSH.length > 0 && selectedSSH.every((row) => row.sshAllowPassword);
  const emailMethod = useWatch({ control: methods.control, name: 'emailMethod' });
  const firstNum = useWatch({ control: methods.control, name: 'firstNum' });
  const flow = useWatch({ control: methods.control, name: 'flow' });
  const expiryTime = useWatch({ control: methods.control, name: 'expiryTime' });
  const subId = useWatch({ control: methods.control, name: 'subId' });
  const limitIp = useWatch({ control: methods.control, name: 'limitIp' });
  const trafficReset = useWatch({ control: methods.control, name: 'trafficReset' });
  const [delayedStart, setDelayedStart] = useState(false);
  const [saving, setSaving] = useState(false);
  const fail2ban = useFail2banStatusQuery();
  const limitIpDisabled = !fail2ban.usable;
  const limitIpNotice = getLimitIpNotice(fail2ban, t);

  const [wasOpen, setWasOpen] = useState(false);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) {
      methods.reset(EMPTY);
      setDelayedStart(false);
    }
  }

  const flowCapableIds = useMemo(() => {
    const ids = new Set<number>();
    for (const row of inbounds || []) {
      if (row?.tlsFlowCapable) ids.add(row.id);
    }
    return ids;
  }, [inbounds]);

  const showFlow = useMemo(
    () => (inboundIds || []).some((id) => flowCapableIds.has(id)),
    [inboundIds, flowCapableIds],
  );

  const ss2022Method = useMemo(() => {
    for (const id of inboundIds || []) {
      const ib = (inbounds || []).find((row) => row.id === id);
      const method = ib?.ssMethod;
      if (method && method.substring(0, 4) === '2022') return method;
    }
    return '';
  }, [inboundIds, inbounds]);

  const tuicIds = useMemo(() => {
    const ids = new Set<number>();
    for (const row of inbounds || []) {
      if (row && row.protocol === 'tuic') ids.add(row.id);
    }
    return ids;
  }, [inbounds]);

  const hasTuic = useMemo(
    () => (inboundIds || []).some((id) => tuicIds.has(id)),
    [inboundIds, tuicIds],
  );

  useEffect(() => {
    if (!showFlow && flow) {
      methods.setValue('flow', '');
    }
  }, [showFlow, flow, methods]);

  const inboundOptions = useMemo(
    () =>
      (inbounds || [])
        .filter((ib) => MULTI_CLIENT_PROTOCOLS.has(ib.protocol || ''))
        .map((ib) => ({
          label: formatInboundLabel(ib.tag, ib.remark),
          value: ib.id,
          disabled: ib.protocol === 'snell' && (ib.snellOwnerCount ?? 0) > 0,
        })),
    [inbounds],
  );

  const expiryDate = useMemo<Dayjs | null>(
    () => (expiryTime > 0 ? dayjs(expiryTime) : null),
    [expiryTime],
  );

  const delayedExpireDays = expiryTime < 0 ? expiryTime / -86400000 : 0;

  function buildEmails(values: ClientBulkAddFormValues): string[] {
    const method = values.emailMethod;
    const out: string[] = [];
    let start: number;
    let end: number;
    if (method > 1) {
      start = values.firstNum;
      end = values.lastNum + 1;
    } else {
      start = 0;
      end = values.quantity;
    }
    const prefix = method > 0 && values.emailPrefix.length > 0 ? values.emailPrefix : '';
    const useNum = method > 1;
    const postfix = method > 2 && values.emailPostfix.length > 0 ? values.emailPostfix : '';
    for (let i = start; i < end; i++) {
      let email = '';
      if (method !== 4) email = RandomUtil.randomLowerAndNum(10);
      email += useNum ? prefix + String(i) + postfix : prefix + postfix;
      out.push(email);
    }
    return out;
  }

  async function submit() {
    const current = methods.getValues();
    const validated = ClientBulkAddFormSchema.safeParse(current);
    if (!validated.success) {
      messageApi.error(t(validated.error.issues[0]?.message ?? 'somethingWentWrong'));
      return;
    }
    const policyRequested =
      current.policyUploadBytesPerSecond != null ||
      current.policyDownloadBytesPerSecond != null ||
      current.policyMultiplier !== '';
    if (policyRequested && policyUnsupported) {
      messageApi.error(t('pages.clients.policy.localOnly'));
      return;
    }
    if (
      selectedSSH.length &&
      !current.sshAuthorizedKeys.trim() &&
      !(current.generateSshPasswords && allSSHAllowPassword)
    ) {
      messageApi.error(t('pages.clients.sshAuthenticationRequired'));
      return;
    }
    const emails = buildEmails(current);
    if (
      hasSnell &&
      (emails.length !== 1 || selectedSnell.some((row) => (row.snellOwnerCount ?? 0) > 0))
    ) {
      messageApi.error(t('pages.clients.snellSingleOwner'));
      return;
    }
    if (emails.length === 0) return;

    setSaving(true);
    try {
      const payloads = emails.map((email) => ({
        client: {
          ...(policyRequested
            ? {
                policy: {
                  uploadBytesPerSecond: current.policyUploadBytesPerSecond ?? 0,
                  downloadBytesPerSecond: current.policyDownloadBytesPerSecond ?? 0,
                  multiplier: current.policyMultiplier,
                },
              }
            : {}),
          email,
          subId: current.subId || RandomUtil.randomLowerAndNum(16),
          id: RandomUtil.randomUUID(),
          password: ss2022Method
            ? RandomUtil.randomShadowsocksPassword(ss2022Method)
            : RandomUtil.randomLowerAndNum(16),
          auth: RandomUtil.randomLowerAndNum(16),
          snellPsk: '',
          mieruUsername: '',
          mieruPassword: '',
          sshUsername: '',
          sshAuthorizedKeys: selectedSSH.length ? current.sshAuthorizedKeys : '',
          sshPassword:
            selectedSSH.length && current.generateSshPasswords && allSSHAllowPassword
              ? RandomUtil.randomUUID().replaceAll('-', '')
              : '',
          flow: showFlow ? current.flow || '' : '',
          totalGB: Math.round((current.totalGB || 0) * SizeFormatter.ONE_GB),
          expiryTime: current.expiryTime,
          reset: Number(current.reset) || 0,
          resetDay: Number(current.resetDay) || 0,
          resetWeekday: Number(current.resetWeekday) || 0,
          resetMax: Number(current.resetMax) || 0,
          trafficReset: current.trafficReset || 'never',
          trafficResetDay: Number(current.trafficResetDay) || 1,
          limitIp: Number(current.limitIp) || 0,
          limitHwid: Number(current.limitHwid) || 0,
          group: current.group,
          comment: current.comment,
          enable: true,
        },
        inboundIds: current.inboundIds,
      }));
      const msg = await bulkCreate(payloads);
      const ok = msg?.obj?.created ?? 0;
      const skipped = msg?.obj?.skipped ?? [];
      const failed = skipped.length;
      const firstError = skipped[0]?.reason ?? msg?.msg ?? '';
      if (failed === 0 && msg?.success) {
        messageApi.success(t('pages.clients.toasts.bulkCreated', { count: ok }));
      } else {
        messageApi.warning(
          firstError
            ? `${t('pages.clients.toasts.bulkCreatedMixed', { ok, failed })} — ${firstError}`
            : t('pages.clients.toasts.bulkCreatedMixed', { ok, failed }),
        );
      }
      onSaved?.();
      onOpenChange(false);
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      {messageContextHolder}
      <Modal
        open={open}
        title={t('pages.clients.bulk')}
        okText={t('create')}
        cancelText={t('close')}
        confirmLoading={saving}
        mask={{ closable: false }}
        width={640}
        onOk={submit}
        onCancel={() => onOpenChange(false)}
      >
        <FormProvider {...methods}>
          <Form colon={false} labelCol={{ sm: { span: 8 } }} wrapperCol={{ sm: { span: 14 } }}>
            <Form.Item label={t('pages.clients.attachedInbounds')} required>
              <SelectAllClearButtons
                options={inboundOptions}
                value={inboundIds}
                onChange={(v) => methods.setValue('inboundIds', v)}
              />
              <Select
                mode="multiple"
                value={inboundIds}
                onChange={(v) => methods.setValue('inboundIds', v)}
                options={inboundOptions}
                placeholder={t('pages.clients.selectInbound')}
                showSearch={{
                  filterOption: (input, option) =>
                    ((option?.label as string) || '').toLowerCase().includes(input.toLowerCase()),
                }}
              />
            </Form.Item>

            {selectedSSH.length > 0 && (
              <>
                <FormField
                  name="sshAuthorizedKeys"
                  label={t('pages.clients.sshAuthorizedKeys')}
                  tooltip={t('pages.clients.sshBulkKeysDesc')}
                >
                  <Input.TextArea rows={3} />
                </FormField>
                {allSSHAllowPassword && (
                  <FormField
                    name="generateSshPasswords"
                    label={t('pages.clients.generateSshPasswords')}
                    valueProp="checked"
                  >
                    <Switch />
                  </FormField>
                )}
              </>
            )}

            <FormField name="emailMethod" label={t('pages.clients.method')}>
              <Select
                disabled={hasSnell}
                options={[
                  { value: 0, label: 'Random' },
                  { value: 1, label: 'Random + Prefix' },
                  { value: 2, label: 'Random + Prefix + Num' },
                  { value: 3, label: 'Random + Prefix + Num + Postfix' },
                  { value: 4, label: 'Prefix + Num + Postfix' },
                ]}
              />
            </FormField>

            {emailMethod > 1 && (
              <>
                <FormField
                  name="firstNum"
                  label={t('pages.clients.first')}
                  transform={{ output: (v) => Number(v) || 1 }}
                >
                  <InputNumber min={1} />
                </FormField>
                <FormField
                  name="lastNum"
                  label={t('pages.clients.last')}
                  transform={{ output: (v) => Number(v) || 1 }}
                >
                  <InputNumber min={firstNum} />
                </FormField>
              </>
            )}
            {emailMethod > 0 && (
              <FormField name="emailPrefix" label={t('pages.clients.prefix')}>
                <Input />
              </FormField>
            )}
            {emailMethod > 2 && (
              <FormField name="emailPostfix" label={t('pages.clients.postfix')}>
                <Input />
              </FormField>
            )}
            {emailMethod < 2 && (
              <FormField
                name="quantity"
                label={t('pages.clients.clientCount')}
                transform={{ output: (v) => Number(v) || 1 }}
              >
                <InputNumber min={1} max={hasSnell ? 1 : 1000} disabled={hasSnell} />
              </FormField>
            )}

            <Form.Item label={t('pages.clients.subId')}>
              <Space.Compact style={{ display: 'flex' }}>
                <Input
                  value={subId}
                  onChange={(e) => methods.setValue('subId', e.target.value)}
                  style={{ flex: 1 }}
                />
                <Button
                  aria-label={t('regenerate')}
                  icon={<ReloadOutlined />}
                  onClick={() => methods.setValue('subId', RandomUtil.randomLowerAndNum(16))}
                />
              </Space.Compact>
            </Form.Item>

            <FormField
              name="group"
              label={t('pages.clients.group')}
              tooltip={t('pages.clients.groupDesc')}
              transform={{ output: (v) => v ?? '' }}
            >
              <AutoComplete
                placeholder={t('pages.clients.groupPlaceholder')}
                options={groups.map((g) => ({ value: g }))}
                allowClear
              />
            </FormField>

            <FormField
              name="limitHwid"
              label={t('pages.clients.limitHwid')}
              tooltip={t('pages.clients.limitHwidDesc')}
              transform={{ output: (v) => Number(v) || 0 }}
            >
              <InputNumber min={0} />
            </FormField>

            <FormField name="comment" label={t('comment')}>
              <Input />
            </FormField>

            {showFlow && (
              <FormField name="flow" label={t('pages.clients.flow')}>
                <Select
                  style={{ width: 220 }}
                  options={[
                    { value: '', label: t('none') },
                    ...FLOW_OPTIONS.map((k) => ({ value: k, label: k })),
                  ]}
                />
              </FormField>
            )}

            <Form.Item label={t('pages.clients.limitIp')}>
              <Tooltip title={limitIpNotice || undefined}>
                <span style={{ display: 'inline-flex' }}>
                  <InputNumber
                    value={limitIp}
                    min={0}
                    disabled={limitIpDisabled}
                    style={limitIpDisabled ? { pointerEvents: 'none' } : undefined}
                    onChange={(v) => methods.setValue('limitIp', Number(v) || 0)}
                  />
                </span>
              </Tooltip>
            </Form.Item>

            <FormField
              name="totalGB"
              label={t('pages.clients.totalGB')}
              tooltip={
                hasTuic ? t('pages.clients.tuicTotalGBDesc') : t('pages.clients.totalGBDesc')
              }
              transform={{ output: (v) => Number(v) || 0 }}
            >
              <InputNumber min={0} step={1} />
            </FormField>

            {policyUnsupported && (
              <Typography.Paragraph type="secondary">
                {t('pages.clients.policy.localOnly')}
              </Typography.Paragraph>
            )}
            <FormField
              name="policyUploadBytesPerSecond"
              label={t('pages.clients.policy.upload')}
              tooltip={t('pages.clients.policy.rateHint')}
            >
              <InputNumber
                disabled={policyUnsupported}
                min={0}
                max={2 ** 40}
                precision={0}
                placeholder="0"
                style={{ width: '100%' }}
              />
            </FormField>
            <FormField
              name="policyDownloadBytesPerSecond"
              label={t('pages.clients.policy.download')}
              tooltip={t('pages.clients.policy.rateHint')}
            >
              <InputNumber
                disabled={policyUnsupported}
                min={0}
                max={2 ** 40}
                precision={0}
                placeholder="0"
                style={{ width: '100%' }}
              />
            </FormField>
            <FormField
              name="policyMultiplier"
              label={t('pages.clients.policy.multiplier')}
              tooltip={t('pages.clients.policy.multiplierHint')}
            >
              <Input disabled={policyUnsupported} placeholder="1" inputMode="decimal" />
            </FormField>

            <Form.Item label={t('pages.clients.delayedStart')}>
              <Switch
                checked={delayedStart}
                onClick={() => {
                  setDelayedStart(!delayedStart);
                  methods.setValue('expiryTime', 0);
                }}
              />
            </Form.Item>

            {delayedStart ? (
              <Form.Item label={t('pages.clients.expireDays')}>
                <InputNumber
                  value={delayedExpireDays}
                  min={0}
                  onChange={(v) => methods.setValue('expiryTime', -86400000 * (Number(v) || 0))}
                />
              </Form.Item>
            ) : (
              <Form.Item label={t('pages.inbounds.expireDate')}>
                <DateTimePicker
                  value={expiryDate}
                  onChange={(next) => methods.setValue('expiryTime', next ? next.valueOf() : 0)}
                />
              </Form.Item>
            )}

            <ClientRenewalFields
              active={open}
              delayedStart={delayedStart}
              expiryTime={expiryTime}
              bulk
              setExpiry={(expiry) => methods.setValue('expiryTime', expiry)}
            />

            <FormField name="trafficReset" label={t('pages.inbounds.periodicTrafficResetTitle')}>
              <Select
                options={TRAFFIC_RESETS.map((r) => ({
                  value: r,
                  label: t(`pages.inbounds.periodicTrafficReset.${r}`),
                }))}
              />
            </FormField>

            {trafficReset === 'monthly' && (
              <FormField
                name="trafficResetDay"
                label={t('pages.inbounds.periodicTrafficResetDay')}
                transform={{ output: (v) => Number(v) || 1 }}
              >
                <InputNumber min={1} max={31} />
              </FormField>
            )}
          </Form>
        </FormProvider>
      </Modal>
    </>
  );
}

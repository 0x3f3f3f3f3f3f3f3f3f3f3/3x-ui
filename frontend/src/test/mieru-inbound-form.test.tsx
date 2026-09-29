import { ConfigProvider } from 'antd';
import { describe, it, expect, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';

import { HttpUtil } from '@/utils';
import InboundFormModal from '@/pages/inbounds/form/InboundFormModal';
import { InboundFormSchema } from '@/schemas/forms/inbound-form';
import { rawInboundToFormValues, formValuesToWirePayload } from '@/lib/xray/inbound-form-adapter';
import { renderWithProviders, chooseSelectOption } from './test-utils';

describe('mieru inbound management', () => {
  it.each([
    { email: '界'.repeat(22), password: 'valid-password' },
    { email: 'padded ', password: 'valid-password' },
    { email: 'native-user', password: '' },
    { email: 'native-user', password: '界'.repeat(22) },
  ])('rejects credentials the native server cannot use', (client) => {
    const result = InboundFormSchema.safeParse(
      rawInboundToFormValues({
        protocol: 'mieru',
        port: 2443,
        settings: { network: 'tcp', clients: [client] },
      }),
    );
    expect(result.success).toBe(false);
  });

  it('edits an empty API listener and preserves its native transport and private port', () => {
    const values = InboundFormSchema.parse(
      rawInboundToFormValues({
        protocol: 'mieru',
        port: 2443,
        settings: { network: 'both', bridgePort: 47123, clients: null },
      }),
    );
    expect(JSON.parse(formValuesToWirePayload(values).settings)).toEqual({
      network: 'both',
      bridgePort: 47123,
      clients: [],
    });
  });

  it.each([
    ['tcp', 'TCP'],
    ['udp', 'UDP'],
    ['both', 'TCP, UDP'],
  ])(
    'creates a native %s listener without Xray stream or security settings',
    async (network, label) => {
      const post = vi
        .spyOn(HttpUtil, 'post')
        .mockResolvedValue({ success: true, msg: '', obj: null });
      renderWithProviders(
        <ConfigProvider virtual={false}>
          <InboundFormModal
            open
            mode="add"
            dbInbound={null}
            dbInbounds={[]}
            availableNodes={[]}
            onClose={() => {}}
            onSaved={() => {}}
          />
        </ConfigProvider>,
      );
      chooseSelectOption('protocol', 'mieru');
      await waitFor(() => expect(screen.queryByRole('tab', { name: 'Stream' })).toBeNull());
      fireEvent.click(screen.getByRole('tab', { name: 'Protocol' }));
      chooseSelectOption('mieru-network', label);
      fireEvent.click(screen.getByRole('tab', { name: 'Basics' }));
      fireEvent.change(screen.getByRole('spinbutton', { name: 'Port' }), {
        target: { value: '2443' },
      });
      const submit = document.querySelector('.ant-modal-footer .ant-btn-primary');
      if (!submit) throw new Error('create button missing');
      fireEvent.click(submit);
      await waitFor(() =>
        expect(post).toHaveBeenCalledWith(
          '/panel/api/inbounds/add',
          expect.objectContaining({
            protocol: 'mieru',
            port: 2443,
            streamSettings: '',
            sniffing: '',
          }),
        ),
      );
      const request = post.mock.calls.find(([url]) => url === '/panel/api/inbounds/add');
      if (!request) throw new Error('mieru creation request missing');
      expect(JSON.parse((request[1] as { settings: string }).settings)).toEqual({
        network,
        clients: [],
      });
      expect(screen.queryByRole('tab', { name: 'Security' })).toBeNull();
      expect(screen.queryByRole('tab', { name: 'Sniffing' })).toBeNull();
    },
  );

  it('preserves credentials, renewal and lifecycle metadata when editing', () => {
    const settings = {
      network: 'udp',
      bridgePort: 47123,
      clients: [
        {
          email: 'mieru-user',
          password: 'fixture-password',
          subId: 'mieru-sub',
          group: 'group-a',
          enable: false,
          totalGB: 1234,
          expiryTime: -86400000,
          limitIp: 3,
          tgId: 123,
          comment: 'client note',
          reset: 1,
          resetDay: 2,
          resetWeekday: 3,
          resetMax: 5,
          trafficReset: 'monthly',
          trafficResetDay: 9,
          created_at: 123,
          updated_at: 456,
        },
      ],
    };
    const values = InboundFormSchema.parse(
      rawInboundToFormValues({
        protocol: 'mieru',
        port: 2443,
        settings,
        streamSettings: { network: 'tcp', security: 'none' },
        sniffing: { enabled: true },
      }),
    );
    const payload = formValuesToWirePayload(values);
    expect(JSON.parse(payload.settings)).toMatchObject(settings);
    expect(payload.streamSettings).toBe('');
    expect(payload.sniffing).toBe('');
  });
});

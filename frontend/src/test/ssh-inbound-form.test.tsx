import { ConfigProvider } from 'antd';
import { describe, it, expect, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';

import { HttpUtil } from '@/utils';
import InboundFormModal from '@/pages/inbounds/form/InboundFormModal';
import { InboundFormSchema } from '@/schemas/forms/inbound-form';
import { rawInboundToFormValues, formValuesToWirePayload } from '@/lib/xray/inbound-form-adapter';
import { renderWithProviders, chooseSelectOption } from './test-utils';

describe('SSH inbound management', () => {
  it('edits an empty API-created listener whose Go clients slice is null', () => {
    const values = InboundFormSchema.parse(
      rawInboundToFormValues({
        protocol: 'ssh',
        port: 2222,
        settings: { hostKey: 'stored-host-key', bridgePort: 47123, clients: null },
      }),
    );
    expect(JSON.parse(formValuesToWirePayload(values).settings)).toEqual({
      hostKey: 'stored-host-key',
      bridgePort: 47123,
      clients: [],
    });
  });

  it('creates a managed SSH listener without Xray transport or TLS settings', async () => {
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
    chooseSelectOption('protocol', 'ssh');
    await waitFor(() => expect(screen.queryByRole('tab', { name: 'Stream' })).toBeNull());
    const port = screen.getByRole('spinbutton', { name: 'Port' });
    fireEvent.change(port, { target: { value: '2222' } });
    const submit = document.querySelector('.ant-modal-footer .ant-btn-primary');
    if (!submit) throw new Error('create button missing');
    fireEvent.click(submit);
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith(
        '/panel/api/inbounds/add',
        expect.objectContaining({
          protocol: 'ssh',
          port: 2222,
          streamSettings: '',
          sniffing: '',
        }),
      ),
    );
    const request = post.mock.calls.find(([url]) => url === '/panel/api/inbounds/add');
    const payload = request?.[1] as { settings: string };
    expect(JSON.parse(payload.settings)).toEqual({ clients: [] });
    expect(screen.queryByRole('tab', { name: 'Security' })).toBeNull();
    expect(screen.queryByRole('tab', { name: 'Sniffing' })).toBeNull();
  });

  it('preserves host identity, credentials and lifecycle fields through edit serialization', () => {
    const settings = {
      hostKey: 'stored-host-key',
      bridgePort: 47123,
      clients: [
        {
          email: 'ssh-user',
          subId: 'ssh-sub',
          enable: false,
          totalGB: 1234,
          expiryTime: -86400000,
          reset: 0,
          resetDay: 0,
          resetWeekday: 3,
          resetMax: 5,
          ssh: {
            publicKeys: [
              'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMlM7FlZhqPVGX/CZdQJcMWTCfcRWAXXrgNKWBGPVH4L',
            ],
            targets: [{ host: 'route.example', port: 443 }],
            reverse: [{ address: '127.0.0.1', port: 8022 }],
          },
        },
      ],
    };
    const values = InboundFormSchema.parse(
      rawInboundToFormValues({
        protocol: 'ssh',
        port: 2222,
        settings,
        streamSettings: {},
        sniffing: { enabled: false },
      }),
    );
    expect(JSON.parse(formValuesToWirePayload(values).settings)).toMatchObject(settings);
  });
});

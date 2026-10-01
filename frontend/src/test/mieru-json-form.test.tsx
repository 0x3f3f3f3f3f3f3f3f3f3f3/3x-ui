import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import OutboundFormModal from '@/pages/xray/outbounds/OutboundFormModal';
import { renderWithProviders } from './test-utils';

vi.mock('@/components/form/JsonEditor', () => ({
  default: ({ value, onChange }: { value: string; onChange: (next: string) => void }) => (
    <textarea
      aria-label="Outbound JSON"
      value={value}
      onChange={(event) => onChange(event.target.value)}
    />
  ),
}));

describe('official mieru JSON in the existing outbound form', () => {
  it('saves the official native profile through the JSON tab', async () => {
    const confirm = vi.fn();
    renderWithProviders(
      <OutboundFormModal
        open
        outbound={null}
        existingTags={[]}
        onClose={() => {}}
        onConfirm={confirm}
      />,
    );
    fireEvent.click(screen.getByRole('tab', { name: 'JSON' }));
    const config = {
      profiles: [
        {
          profileName: 'native UDP',
          user: { name: '用户', password: '密码' },
          servers: [
            { domainName: 'native.example.test', portBindings: [{ port: 8443, protocol: 'UDP' }] },
          ],
          mtu: 1400,
          multiplexing: { level: 'MULTIPLEXING_HIGH' },
        },
      ],
      activeProfile: 'native UDP',
      socks5Port: 1080,
    };
    fireEvent.change(await screen.findByRole('textbox', { name: 'Outbound JSON' }), {
      target: { value: JSON.stringify(config) },
    });
    await act(async () =>
      fireEvent.click(document.querySelector('.ant-modal-footer .ant-btn-primary')!),
    );
    await waitFor(() => expect(confirm).toHaveBeenCalledTimes(1));
    expect(confirm.mock.calls[0][0]).toMatchObject({
      protocol: 'mieru',
      tag: 'native UDP',
      settings: {
        address: 'native.example.test',
        port: 8443,
        transport: 'UDP',
        username: '用户',
        password: '密码',
        mtu: 1400,
        multiplexing: 'MULTIPLEXING_HIGH',
      },
    });
  });
});

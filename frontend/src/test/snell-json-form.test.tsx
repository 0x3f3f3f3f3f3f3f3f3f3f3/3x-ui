import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import OutboundFormModal from '@/pages/xray/outbounds/OutboundFormModal';
import { renderWithProviders } from './test-utils';
vi.mock('@/components/form/JsonEditor', () => ({
  default: ({ value, onChange }: { value: string; onChange: (value: string) => void }) => (
    <textarea aria-label="Outbound JSON" value={value} onChange={(e) => onChange(e.target.value)} />
  ),
}));
const native = {
  protocol: 'snell',
  tag: 'native',
  settings: {
    version: 5,
    address: 'native.example.test',
    port: 443,
    psk: '独立,quoted"\\native-key',
    obfs: 'http',
    obfsHost: 'front.example.test',
    obfsUri: '/native',
    reuse: false,
    quic: true,
  },
};
describe('Snell outbound JSON in the existing modal', () => {
  it('rejects explicit UDP mux even when global mux is disabled', async () => {
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
    fireEvent.change(await screen.findByRole('textbox', { name: 'Outbound JSON' }), {
      target: {
        value: JSON.stringify({ ...native, mux: { enabled: false, xudpConcurrency: 16 } }),
      },
    });
    await act(async () =>
      fireEvent.click(document.querySelector('.ant-modal-footer .ant-btn-primary')!),
    );
    await screen.findByText(
      'Snell uses its native transport and does not support TLS/REALITY, transport wrappers or global mux.',
    );
    expect(confirm).not.toHaveBeenCalled();
  });
  it('preserves all native settings through JSON and Basics', async () => {
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
    fireEvent.change(await screen.findByRole('textbox', { name: 'Outbound JSON' }), {
      target: { value: JSON.stringify(native) },
    });
    fireEvent.click(screen.getByRole('tab', { name: 'Basics' }));
    await act(async () =>
      fireEvent.click(document.querySelector('.ant-modal-footer .ant-btn-primary')!),
    );
    await waitFor(() => expect(confirm).toHaveBeenCalledTimes(1));
    expect(confirm.mock.calls[0][0]).toMatchObject(native);
  });
});

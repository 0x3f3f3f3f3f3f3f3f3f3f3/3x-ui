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
const KEY =
  'ssh-ed25519 ' +
  btoa(
    String.fromCharCode(0, 0, 0, 11) +
      'ssh-ed25519' +
      String.fromCharCode(0, 0, 0, 32) +
      'k'.repeat(32),
  );
const settings = {
  address: 'native.example.test',
  port: 2222,
  username: 'user',
  password: 'secret',
  hostKey: KEY,
};
describe('native SSH outbound JSON in the existing modal', () => {
  it('rejects explicit UDP mux options even when global mux is disabled', async () => {
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
        value: JSON.stringify({
          protocol: 'ssh',
          tag: 'native',
          settings,
          mux: { enabled: false, xudpConcurrency: 16, xudpProxyUDP443: 'allow' },
        }),
      },
    });
    await act(async () =>
      fireEvent.click(document.querySelector('.ant-modal-footer .ant-btn-primary')!),
    );
    await screen.findByText(
      'SSH supports native TCP forwarding with no UDP, TLS/REALITY, transport wrappers or global mux.',
    );
    expect(confirm).not.toHaveBeenCalled();
  });
  it('preserves a pinned business-key outbound through JSON reopen and save', async () => {
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
    const native = {
      protocol: 'ssh',
      tag: 'native',
      settings: {
        ...settings,
        password: '',
        privateKeyFile: '/var/lib/x-ui/native-ssh/outbound/user.pem',
      },
    };
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

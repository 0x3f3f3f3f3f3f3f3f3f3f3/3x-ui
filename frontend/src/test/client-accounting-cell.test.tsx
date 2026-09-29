import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import ClientTrafficCell from '@/components/clients/ClientTrafficCell';
import { ThemeProvider } from '@/hooks/useTheme';
import type { ClientPolicyAccounting } from '@/schemas/client';
import InboundInfoModal from '@/pages/inbounds/info/InboundInfoModal';
import { acknowledgedAccounting, pendingAccounting } from './fixtures/client-policy-accounting';

describe('confirmed client accounting', () => {
  it('keeps the configured quota in legacy details when their statistics lag', async () => {
    render(
      <ThemeProvider>
        <InboundInfoModal
          open
          onClose={() => {}}
          dbInbound={{
            id: 1,
            address: '127.0.0.1',
            listen: '127.0.0.1',
            port: 12345,
            protocol: 'vless',
            remark: 'Legacy details',
            enable: true,
            settings: {
              clients: [
                {
                  email: 'legacy@example.test',
                  id: pendingAccounting.clientId,
                  enable: true,
                  totalGB: 100,
                },
              ],
            },
            streamSettings: { network: 'tcp', security: 'none' },
            sniffing: {},
            clientStats: [
              { email: 'legacy@example.test', up: 0, down: 0, total: 0, expiryTime: 0 },
            ],
          }}
        />
      </ThemeProvider>,
    );
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText('100 B').textContent).toBe('100 B');
  });

  it('uses the same billed window in inbound client details', async () => {
    render(
      <ThemeProvider>
        <InboundInfoModal
          open
          onClose={() => {}}
          dbInbound={{
            id: 1,
            address: '127.0.0.1',
            listen: '127.0.0.1',
            port: 12345,
            protocol: 'vless',
            remark: 'Accounting details',
            enable: true,
            settings: {
              clients: [
                {
                  email: 'details@example.test',
                  id: pendingAccounting.clientId,
                  enable: true,
                  totalGB: 100,
                },
              ],
            },
            streamSettings: { network: 'tcp', security: 'none' },
            sniffing: {},
            clientStats: [
              {
                email: 'details@example.test',
                up: 50,
                down: 50,
                total: 100,
                expiryTime: 0,
                accounting: pendingAccounting,
              },
            ],
          }}
        />
      </ThemeProvider>,
    );
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).queryByText('Ended')).toBeNull();
    expect(within(dialog).getByRole('progressbar').getAttribute('aria-valuenow')).toBe('20');
    expect(within(dialog).getByText('80 B').textContent).toBe('80 B');
  });

  it('renders billed quota and applies an acknowledged reset without changing legacy counters', async () => {
    const cell = (accounting: ClientPolicyAccounting) => (
      <ThemeProvider>
        <ClientTrafficCell up={50} down={50} total={50} accounting={accounting} />
      </ThemeProvider>
    );
    const view = render(cell(pendingAccounting));
    expect(screen.getByRole('progressbar').getAttribute('aria-valuenow')).toBe('20');
    expect(
      screen
        .getByRole('status', { name: 'Reset awaiting core confirmation' })
        .getAttribute('title'),
    ).toBe('Reset awaiting core confirmation');
    fireEvent.click(screen.getByRole('progressbar'));
    await screen.findByText('Lifetime billed usage');
    expect(screen.getByTitle('50 B').textContent).toContain('50');

    view.rerender(cell(acknowledgedAccounting));
    await waitFor(() =>
      expect(screen.getByRole('progressbar').getAttribute('aria-valuenow')).toBe('0'),
    );
    expect(screen.queryByRole('status', { name: 'Reset awaiting core confirmation' })).toBeNull();
    expect(screen.getByTitle('50 B').textContent).toContain('50');
  });
});

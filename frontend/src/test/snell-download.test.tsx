import { describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import ClientInfoModal from '@/pages/clients/ClientInfoModal';
import { ClipboardManager, FileManager, HttpUtil } from '@/utils';
import { renderWithProviders } from './test-utils';

describe('native Snell downloads through existing client info', () => {
  it('downloads actual Surge and Custom Xray files without inventing a share URI', async () => {
    vi.spyOn(HttpUtil, 'get').mockResolvedValue({ success: true, obj: [] } as never);
    const fetch = vi.fn().mockResolvedValue({ ok: true, text: async () => 'native Snell export' });
    vi.stubGlobal('fetch', fetch);
    const download = vi.spyOn(FileManager, 'downloadTextFile').mockImplementation(() => {});
    const copy = vi.spyOn(ClipboardManager, 'copyText').mockResolvedValue(true);
    renderWithProviders(
      <ClientInfoModal
        open
        client={{
          id: 19,
          email: 'owner',
          subId: 'native-sub',
          enable: true,
          inboundIds: [12],
          snellPsk: 'independent-native-psk',
        }}
        inboundsById={{
          12: { id: 12, protocol: 'snell', tag: 'native', snellVersion: 6, snellOwnerCount: 1 },
        }}
        isOnline={false}
        onOpenChange={() => {}}
        subSettings={{
          enable: true,
          subURI: 'https://panel.test/sub/',
          subJsonURI: '',
          subJsonEnable: false,
          subClashURI: '',
          subClashEnable: false,
        }}
      />,
    );
    for (const [label, format, filename] of [
      ['Download Snell Surge configuration', 'snell-surge', 'snell-surge.conf'],
      ['Download Snell Custom Xray JSON', 'snell-json', 'snell-client.json'],
    ]) {
      const button = await screen.findByRole('button', { name: label });
      const row = button.closest('.link-row')!;
      fireEvent.click(within(row as HTMLElement).getByRole('button', { name: 'Copy' }));
      await waitFor(() =>
        expect(copy).toHaveBeenCalledWith(`https://panel.test/sub/native-sub?format=${format}`),
      );
      fireEvent.click(button);
      await waitFor(() =>
        expect(fetch).toHaveBeenCalledWith(`https://panel.test/sub/native-sub?format=${format}`),
      );
      await waitFor(() => expect(download).toHaveBeenCalledWith('native Snell export', filename));
    }
    expect(document.body.textContent).not.toContain('snell://');
    await screen.findByText(/Surge iOS 5\.21.*Mac 6\.8/);
    vi.unstubAllGlobals();
  });
});

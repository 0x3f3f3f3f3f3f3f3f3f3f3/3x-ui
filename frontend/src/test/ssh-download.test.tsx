import { describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import ClientInfoModal from '@/pages/clients/ClientInfoModal';
import { ClipboardManager, FileManager, HttpUtil } from '@/utils';
import { renderWithProviders } from './test-utils';
describe('native OpenSSH downloads through existing client info', () => {
  it('downloads OpenSSH config, pinned known_hosts and forwarding instructions', async () => {
    vi.spyOn(HttpUtil, 'get').mockResolvedValue({ success: true, obj: [] } as never);
    const fetch = vi
      .fn()
      .mockResolvedValue({ ok: true, text: async () => 'native OpenSSH export' });
    vi.stubGlobal('fetch', fetch);
    const download = vi.spyOn(FileManager, 'downloadTextFile').mockImplementation(() => {});
    const copy = vi.spyOn(ClipboardManager, 'copyText').mockResolvedValue(true);
    renderWithProviders(
      <ClientInfoModal
        open
        client={{
          id: 9,
          email: 'label',
          subId: 'native-sub',
          enable: true,
          inboundIds: [12],
          sshUsername: 'user',
          sshAuthorizedKeys: 'user-provided-public-key',
        }}
        inboundsById={{
          12: { id: 12, protocol: 'ssh', tag: 'native', sshHostFingerprint: 'SHA256:business' },
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
      ['Download OpenSSH configuration', 'ssh', 'x-ui-ssh.conf'],
      ['Download SSH known_hosts', 'ssh-known-hosts', 'x-ui-ssh-known_hosts'],
      ['Download SSH forwarding instructions', 'ssh-instructions', 'x-ui-ssh-instructions.txt'],
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
      await waitFor(() => expect(download).toHaveBeenCalledWith('native OpenSSH export', filename));
    }
    vi.unstubAllGlobals();
  });
});

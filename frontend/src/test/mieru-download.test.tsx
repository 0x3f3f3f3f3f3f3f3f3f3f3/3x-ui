import { describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';

import ClientInfoModal from '@/pages/clients/ClientInfoModal';
import { FileManager, HttpUtil } from '@/utils';
import { renderWithProviders } from './test-utils';

describe('native mieru client configuration download', () => {
  it('downloads the official JSON through the existing subscription endpoint', async () => {
    vi.spyOn(HttpUtil, 'get').mockResolvedValue({
      success: true,
      obj: ['mierus://user:pass@native.test?profile=native&port=8443&protocol=UDP'],
    } as never);
    const body =
      '{"profiles":[{"profileName":"native"}],"activeProfile":"native","socks5Port":1080}';
    const fetch = vi.fn().mockResolvedValue({ ok: true, text: async () => body });
    vi.stubGlobal('fetch', fetch);
    const download = vi.spyOn(FileManager, 'downloadTextFile').mockImplementation(() => {});
    renderWithProviders(
      <ClientInfoModal
        open
        client={{ id: 9, email: 'label', subId: 'native-sub', enable: true }}
        inboundsById={{}}
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
    const button = await screen.findByRole('button', { name: 'Download mieru configuration' });
    fireEvent.click(button);
    await waitFor(() =>
      expect(fetch).toHaveBeenCalledWith('https://panel.test/sub/native-sub?format=mieru'),
    );
    expect(download).toHaveBeenCalledWith(body, 'mieru-client.json');
    vi.unstubAllGlobals();
  });
});

import { fireEvent, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import QrPanel from '@/pages/inbounds/qr/QrPanel';
import { FileManager } from '@/utils';
import { renderWithProviders } from './test-utils';

afterEach(() => vi.restoreAllMocks());

describe('mieru native configuration download', () => {
  it('downloads a loopback SOCKS config from the actual shared profile', () => {
    const download = vi.spyOn(FileManager, 'downloadTextFile').mockImplementation(() => {});
    renderWithProviders(
      <QrPanel value="mierus://native-user:fixture%3Ap%40ss%2F%23%3F%E4%B8%AD%E6%96%87@[2001:db8::17]?port=8443&port=8443&profile=native+profile&protocol=TCP&protocol=UDP" />,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Download mieru JSON' }));
    const [text, filename] = download.mock.calls[0];
    expect(filename).toBe('mieru.json');
    if (typeof text !== 'string') throw new Error('native config download is not text');
    expect(JSON.parse(text)).toEqual({
      profiles: [
        {
          profileName: 'native profile',
          user: { name: 'native-user', password: 'fixture:p@ss/#?中文' },
          servers: [
            {
              ipAddress: '2001:db8::17',
              portBindings: [
                { port: 8443, protocol: 'TCP' },
                { port: 8443, protocol: 'UDP' },
              ],
            },
          ],
        },
      ],
      activeProfile: 'native profile',
      socks5Port: 1080,
      socks5ListenLAN: false,
    });
    expect(screen.getByText(/Xray JSON and legacy Clash do not support mieru/)).not.toBeNull();
  });

  it.each([
    'mierus://u:p@edge.example?profile=native&port=0&protocol=TCP',
    'mierus://u:p@edge.example?profile=native&port=443&protocol=TCP&protocol=UDP',
    'mierus://u:p@edge.example?profile=native&port=443&protocol=TCP&traffic-pattern=unknown',
  ])('does not silently transform an incomplete or unsupported profile: %s', (link) => {
    renderWithProviders(<QrPanel value={link} />);
    expect(screen.queryByRole('button', { name: 'Download mieru JSON' })).toBeNull();
  });
});

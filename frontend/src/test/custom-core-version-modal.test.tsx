import { fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import VersionModal from '@/pages/index/VersionModal';
import { Status } from '@/models/status';
import { HttpUtil, Msg } from '@/utils';
import { renderWithProviders } from './test-utils';

afterEach(() => vi.restoreAllMocks());

function renderModal() {
  return renderWithProviders(
    <VersionModal
      open
      status={new Status({ xray: { state: 'running', version: '26.9.9' } })}
      onClose={vi.fn()}
      onBusy={vi.fn()}
    />,
  );
}

describe('custom core version window', () => {
  it('explains the paired package upgrade without offering official core replacements', async () => {
    vi.spyOn(HttpUtil, 'get').mockResolvedValue(new Msg(true, '', ['v26.9.9']));
    renderModal();
    await screen.findByText(
      'Upgrade the matching panel and Custom Xray-core package to retain Snell, mieru and SSH support.',
    );
    expect(screen.getByText('26.9.9')).toBeTruthy();
    expect(screen.queryByRole('radio')).toBeNull();
    expect(screen.queryByText('v26.9.9')).toBeNull();
  });

  it('retains the existing geofile update confirmation and request', async () => {
    vi.spyOn(HttpUtil, 'get').mockResolvedValue(new Msg(true, '', []));
    const update = vi.spyOn(HttpUtil, 'post').mockResolvedValue(new Msg(true));
    renderModal();
    fireEvent.click(screen.getByText('Geofiles'));
    fireEvent.click(await screen.findByRole('button', { name: 'Update all' }));
    const confirmation = await screen.findByRole('button', { name: 'Confirm' });
    fireEvent.click(confirmation);
    await waitFor(() => expect(update).toHaveBeenCalledWith('/panel/api/server/updateGeofile'));
  });
});

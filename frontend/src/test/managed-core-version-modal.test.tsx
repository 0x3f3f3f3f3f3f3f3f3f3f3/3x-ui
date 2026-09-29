import { fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';

import VersionModal from '@/pages/index/VersionModal';
import { Status } from '@/models/status';
import { HttpUtil, Msg } from '@/utils';
import { renderWithProviders } from './test-utils';

afterEach(() => vi.restoreAllMocks());

it('shows managed package tags separately from the running core and installs the selected tag', async () => {
  const get = vi.spyOn(HttpUtil, 'get').mockResolvedValue(
    new Msg(true, '', [
      { tag: 'v3.8.5-managed.1', prerelease: false },
      { tag: 'dev-latest', prerelease: true },
    ]),
  );
  const post = vi.spyOn(HttpUtil, 'post').mockResolvedValue(new Msg(true, '', null));
  const busy = vi.fn();
  renderWithProviders(
    <VersionModal
      open
      status={new Status({ xray: { state: 'running', version: '26.9.9' } })}
      onClose={vi.fn()}
      onBusy={busy}
    />,
  );
  expect(await screen.findByText('v3.8.5-managed.1')).toBeTruthy();
  expect(get).toHaveBeenCalledWith('/panel/api/server/getManagedCoreReleases');
  expect(screen.getByText('Running Xray: 26.9.9')).toBeTruthy();
  expect(screen.getByText('Prerelease')).toBeTruthy();
  expect(screen.queryByRole('radio')).toBeNull();
  fireEvent.click(screen.getAllByRole('button', { name: 'Install core' })[0]);
  expect(
    await screen.findByText(/Install the core from release package v3.8.5-managed.1/),
  ).toBeTruthy();
  fireEvent.click(screen.getByRole('button', { name: 'Confirm' }));
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith('/panel/api/server/installXray/v3.8.5-managed.1'),
  );
  await waitFor(() => expect(busy).toHaveBeenLastCalledWith({ busy: false }));
});

it('clears old package choices after the catalog becomes unavailable', async () => {
  const get = vi
    .spyOn(HttpUtil, 'get')
    .mockResolvedValue(new Msg(true, '', [{ tag: 'cached-package', prerelease: false }]));
  const props = { status: new Status(), onClose: vi.fn(), onBusy: vi.fn() };
  const view = renderWithProviders(<VersionModal open {...props} />);
  expect(await screen.findByText('cached-package')).toBeTruthy();
  view.rerender(<VersionModal open={false} {...props} />);
  get.mockResolvedValue(new Msg(false, 'Update the container image.', null));
  view.rerender(<VersionModal open {...props} />);
  expect(await screen.findByText('Update the container image.')).toBeTruthy();
  expect(screen.queryByText('cached-package')).toBeNull();
  expect(screen.queryByRole('button', { name: 'Install core' })).toBeNull();
});

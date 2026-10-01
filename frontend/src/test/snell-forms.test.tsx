import { describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import ClientFormModal from '@/pages/clients/ClientFormModal';
import InboundFormModal from '@/pages/inbounds/form/InboundFormModal';
import OutboundFormModal from '@/pages/xray/outbounds/OutboundFormModal';
import { DBInbound } from '@/models/dbinbound';
import type { InboundOption } from '@/schemas/client';
import { HttpUtil } from '@/utils';
import { renderWithProviders, fieldLabels, chooseSelectOption } from './test-utils';

const psk = '独立,quoted"\\native-key';
const listener = {
  id: 19,
  protocol: 'snell',
  port: 443,
  enable: true,
  tag: 'snell-native',
  snellVersion: 6,
  snellOwnerCount: 1,
  snellOwnerClientId: 'owner-id',
} as InboundOption;
function input(label: string): HTMLInputElement {
  const field = Array.from(document.querySelectorAll('.ant-form-item')).find(
    (el) => el.querySelector('.ant-form-item-label label')?.textContent?.trim() === label,
  );
  const control = field?.querySelector<HTMLInputElement>('input');
  if (!control) throw new Error(`Missing field: ${label}`);
  return control;
}
function credentials() {
  fireEvent.click(screen.getByRole('tab', { name: 'Credentials' }));
}
function attachSnell() {
  const field = Array.from(document.querySelectorAll('.ant-form-item')).find(
    (el) =>
      el.querySelector('.ant-form-item-label label')?.textContent?.trim() === 'Attached inbounds',
  )!;
  const select = field.querySelector('.ant-select')!;
  fireEvent.mouseDown(select.querySelector('.ant-select-selector') ?? select);
  const option = Array.from(document.querySelectorAll('.ant-select-item-option')).find((el) =>
    el.textContent?.includes('snell-native'),
  )!;
  expect(option).toBeTruthy();
  return option;
}

describe('native Snell existing public forms', () => {
  it('creates an owner for a staged listener with server-generated independent PSK', async () => {
    const save = vi.fn().mockResolvedValue({ success: true });
    renderWithProviders(
      <ClientFormModal
        open
        mode="add"
        client={null}
        inbounds={[
          { ...listener, enable: false, snellOwnerCount: 0, snellOwnerClientId: undefined },
        ]}
        attachedIds={[]}
        save={save}
        onOpenChange={() => {}}
      />,
    );
    fireEvent.click(attachSnell());
    credentials();
    fireEvent.change(input('Email'), { target: { value: 'new-owner' } });
    expect(input('Snell PSK').value).toBe('');
    fireEvent.click(await screen.findByRole('button', { name: /create/i }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0][0]).toMatchObject({
      inboundIds: [19],
      client: { email: 'new-owner', snellPsk: '' },
    });
  });
  it('prevents attaching another account to a disabled reserved listener', () => {
    renderWithProviders(
      <ClientFormModal
        open
        mode="edit"
        client={{ clientId: 'other-id', email: 'other', enable: true }}
        inbounds={[{ ...listener, enable: false }]}
        attachedIds={[]}
        save={vi.fn()}
        onOpenChange={() => {}}
      />,
    );
    expect(attachSnell().classList.contains('ant-select-item-option-disabled')).toBe(true);
  });
  it('leaves a blank PSK for the server to preserve on an existing account', async () => {
    const save = vi.fn().mockResolvedValue({ success: true });
    renderWithProviders(
      <ClientFormModal
        open
        mode="edit"
        client={{ clientId: 'owner-id', email: 'owner', enable: true, snellPsk: psk }}
        inbounds={[listener]}
        attachedIds={[19]}
        save={save}
        onOpenChange={() => {}}
      />,
    );
    credentials();
    fireEvent.change(input('Snell PSK'), { target: { value: '' } });
    fireEvent.click(await screen.findByRole('button', { name: /save/i }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0][0]).toMatchObject({ snellPsk: '' });
  });
  it('binds a selected canonical owner without accepting a raw runtime identity', async () => {
    const ownerId = '97af23ee-90d3-48d3-bb9f-790322466bea';
    vi.mocked(HttpUtil.get).mockImplementation(async (url) => ({
      success: true,
      msg: '',
      obj: String(url).includes('/clients/list/paged')
        ? {
            items: [{ clientId: ownerId, email: 'selected-owner' }],
            total: 1,
            filtered: 1,
            page: 1,
            pageSize: 25,
          }
        : [],
    }));
    const inbound = new DBInbound({
      id: 19,
      protocol: 'snell',
      port: 443,
      enable: false,
      settings: { version: 4, clients: [], obfs: 'off', quic: false, mode: '' },
      streamSettings: {},
    });
    const post = vi
      .spyOn(HttpUtil, 'post')
      .mockResolvedValue({ success: true, msg: '', obj: inbound } as never);
    renderWithProviders(
      <InboundFormModal
        open
        mode="edit"
        dbInbound={inbound}
        dbInbounds={[inbound]}
        availableNodes={[]}
        onClose={() => {}}
        onSaved={() => {}}
      />,
    );
    fireEvent.mouseDown((await screen.findByLabelText('Owner account')).closest('.ant-select')!);
    await screen.findByText('selected-owner', { selector: '.ant-select-item-option-content' });
    chooseSelectOption('ownerClientId', 'selected-owner');
    await act(async () =>
      fireEvent.click(document.querySelector('.ant-modal-footer .ant-btn-primary')!),
    );
    await waitFor(() => expect(post).toHaveBeenCalled());
    const payload = post.mock.calls.at(-1)![1] as Record<string, unknown>;
    expect(payload.ownerClientId).toBe(ownerId);
    const settings = JSON.parse(payload.settings as string);
    expect(settings).not.toHaveProperty('clients');
    expect(settings).not.toHaveProperty('clientId');
    expect(settings).not.toHaveProperty('psk');
  });
  it('renames its owner without changing any independent protocol credential', async () => {
    const save = vi.fn().mockResolvedValue({ success: true });
    renderWithProviders(
      <ClientFormModal
        open
        mode="edit"
        client={{
          clientId: 'owner-id',
          email: 'owner',
          enable: true,
          snellPsk: psk,
          password: 'ordinary',
          sshPassword: 'ssh-secret',
          mieruPassword: 'mieru-secret',
        }}
        inbounds={[listener]}
        attachedIds={[19]}
        save={save}
        onOpenChange={() => {}}
      />,
    );
    credentials();
    await waitFor(() => expect(input('Snell PSK').value).toBe(psk));
    fireEvent.change(input('Email'), { target: { value: 'renamed' } });
    fireEvent.click(await screen.findByRole('button', { name: /save/i }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0][0]).toMatchObject({
      email: 'renamed',
      snellPsk: psk,
      password: 'ordinary',
      sshPassword: 'ssh-secret',
      mieruPassword: 'mieru-secret',
    });
  });
  it('rejects a short explicit PSK on a v6 service', async () => {
    const save = vi.fn();
    renderWithProviders(
      <ClientFormModal
        open
        mode="edit"
        client={{ clientId: 'owner-id', email: 'owner', enable: true, snellPsk: psk }}
        inbounds={[listener]}
        attachedIds={[19]}
        save={save}
        onOpenChange={() => {}}
      />,
    );
    credentials();
    fireEvent.change(input('Snell PSK'), { target: { value: 'short' } });
    fireEvent.click(await screen.findByRole('button', { name: /save/i }));
    await screen.findByText('Snell v6 requires a PSK of at least 12 UTF-8 bytes.');
    expect(save).not.toHaveBeenCalled();
  });
  it.each([4, 5, 6])(
    'reopens a version%d owner without transport or security wrappers',
    async (version) => {
      const settings = {
        version,
        clients: [{ email: 'owner', snellPsk: psk, enable: false }],
        obfs: 'off',
        mode: version === 6 ? 'unshaped' : '',
        quic: version === 5,
      };
      const inbound = new DBInbound({
        id: 19,
        protocol: 'snell',
        port: 443,
        enable: true,
        settings,
        streamSettings: {},
        sniffing: {},
      });
      const post = vi
        .spyOn(HttpUtil, 'post')
        .mockResolvedValue({ success: true, msg: '', obj: inbound } as never);
      renderWithProviders(
        <InboundFormModal
          open
          mode="edit"
          dbInbound={inbound}
          dbInbounds={[inbound]}
          availableNodes={[]}
          onClose={() => {}}
          onSaved={() => {}}
        />,
      );
      await waitFor(() => expect(fieldLabels()).toContain('Snell version'));
      expect(fieldLabels()).toContain('Owner account');
      expect(fieldLabels()).not.toContain('Security');
      expect(fieldLabels()).not.toContain('Transmission');
      expect(fieldLabels()).not.toContain('Native QUIC');
      await act(async () =>
        fireEvent.click(document.querySelector('.ant-modal-footer .ant-btn-primary')!),
      );
      await waitFor(() => expect(post).toHaveBeenCalled());
      expect(
        JSON.parse((post.mock.calls.at(-1)![1] as Record<string, unknown>).settings as string),
      ).toMatchObject(settings);
    },
  );
  it('saves the explicit native outbound endpoint and independent PSK', async () => {
    const settings = {
      version: 5,
      address: 'native.example.test',
      port: 443,
      psk,
      obfs: 'http',
      obfsHost: 'front.example.test',
      obfsUri: '/native',
      reuse: false,
      quic: true,
    };
    const confirm = vi.fn();
    renderWithProviders(
      <OutboundFormModal
        open
        outbound={{ protocol: 'snell', tag: 'native', settings }}
        existingTags={[]}
        onClose={() => {}}
        onConfirm={confirm}
      />,
    );
    await waitFor(() => expect(input('Snell PSK').value).toBe(psk));
    expect(fieldLabels()).not.toContain('Security');
    await act(async () =>
      fireEvent.click(document.querySelector('.ant-modal-footer .ant-btn-primary')!),
    );
    await waitFor(() => expect(confirm).toHaveBeenCalledTimes(1));
    expect(confirm.mock.calls[0][0]).toMatchObject({ protocol: 'snell', settings });
    expect(confirm.mock.calls[0][0]).not.toHaveProperty('mux');
  });
  it('switches from v5 HTTP obfuscation to a valid native v6 outbound', async () => {
    const confirm = vi.fn();
    renderWithProviders(
      <OutboundFormModal
        open
        outbound={{
          protocol: 'snell',
          tag: 'native',
          settings: {
            version: 5,
            address: 'native.example.test',
            port: 443,
            psk,
            obfs: 'http',
            obfsHost: 'front.example.test',
            obfsUri: '/native',
            reuse: true,
            quic: true,
          },
        }}
        existingTags={[]}
        onClose={() => {}}
        onConfirm={confirm}
      />,
    );
    await waitFor(() => expect(fieldLabels()).toContain('Snell version'));
    chooseSelectOption((await screen.findByLabelText('Snell version')).id, 'v6');
    await act(async () =>
      fireEvent.click(document.querySelector('.ant-modal-footer .ant-btn-primary')!),
    );
    await waitFor(() => expect(confirm).toHaveBeenCalledTimes(1));
    expect(confirm.mock.calls[0][0].settings).toMatchObject({
      version: 6,
      psk,
      obfs: 'off',
      obfsHost: '',
      obfsUri: '',
      quic: false,
    });
  });
});

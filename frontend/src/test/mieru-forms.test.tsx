import { describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor } from '@testing-library/react';

import ClientFormModal from '@/pages/clients/ClientFormModal';
import InboundFormModal from '@/pages/inbounds/form/InboundFormModal';
import OutboundFormModal from '@/pages/xray/outbounds/OutboundFormModal';
import { DBInbound } from '@/models/dbinbound';
import type { InboundOption } from '@/schemas/client';
import { HttpUtil } from '@/utils';
import { chooseSelectOption, fieldLabels, renderWithProviders } from './test-utils';

const MIERU_INBOUND = {
  id: 9,
  protocol: 'mieru',
  port: 8443,
  tag: 'native',
  enable: true,
} as InboundOption;

function credentialInput(label: string): HTMLInputElement {
  const field = Array.from(document.querySelectorAll('.ant-form-item')).find(
    (el) => el.querySelector('.ant-form-item-label label')?.textContent?.trim() === label,
  );
  const input = field?.querySelector('input');
  if (!input) throw new Error(`Missing credential field: ${label}`);
  return input;
}

function fieldId(label: string): string {
  const field = Array.from(
    document.querySelectorAll<HTMLLabelElement>('.ant-form-item-label label'),
  ).find((el) => el.textContent?.trim() === label);
  if (!field?.htmlFor) throw new Error(`Missing field: ${label}`);
  return field.htmlFor;
}

function credentialsTab() {
  const tab = Array.from(document.querySelectorAll('.ant-tabs-tab')).find(
    (el) => el.textContent?.trim() === 'Credentials',
  );
  if (!tab) throw new Error('Missing credentials tab');
  fireEvent.click(tab);
}

describe('native mieru public forms', () => {
  it('reopens and saves a UDP listener with all native options', async () => {
    const settings = {
      transport: 'UDP',
      mtu: 1400,
      userHintRequired: true,
      maxConnections: 32,
      handshakeTimeoutSeconds: 8,
      clients: [],
    };
    const inbound = new DBInbound({
      id: 9,
      protocol: 'mieru',
      port: 8443,
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
    await waitFor(() => expect(fieldLabels()).toContain('Handshake timeout (seconds)'));
    expect(document.getElementById(fieldId('Transport'))).toBeTruthy();
    expect(fieldLabels()).not.toContain('Security');
    await act(async () =>
      fireEvent.click(document.querySelector('.ant-modal-footer .ant-btn-primary')!),
    );
    await waitFor(() => expect(post).toHaveBeenCalled());
    const body = post.mock.calls.at(-1)![1] as Record<string, unknown>;
    expect(JSON.parse(body.settings as string)).toMatchObject(settings);
  });

  it('edits native outbound transport, credentials and multiplexing', async () => {
    const settings = {
      address: '2001:db8::1',
      port: 8443,
      transport: 'TCP',
      mtu: 1400,
      username: '独立-user',
      password: '独立-password',
      multiplexing: 'MULTIPLEXING_HIGH',
    };
    const confirm = vi.fn();
    renderWithProviders(
      <OutboundFormModal
        open
        outbound={{ protocol: 'mieru', tag: 'native', settings }}
        existingTags={[]}
        onClose={() => {}}
        onConfirm={confirm}
      />,
    );
    await waitFor(() => expect(fieldLabels()).toContain('Multiplexing'));
    expect(credentialInput('mieru username').value).toBe(settings.username);
    chooseSelectOption(fieldId('Transport'), 'UDP');
    await act(async () =>
      fireEvent.click(document.querySelector('.ant-modal-footer .ant-btn-primary')!),
    );
    await waitFor(() => expect(confirm).toHaveBeenCalledTimes(1));
    expect(confirm.mock.calls[0][0]).toMatchObject({
      protocol: 'mieru',
      settings: { ...settings, transport: 'UDP' },
    });
    expect(confirm.mock.calls[0][0]).not.toHaveProperty('mux');
  });

  it('renames a shared client while preserving independent stored credentials', async () => {
    const save = vi.fn().mockResolvedValue({ success: true });
    renderWithProviders(
      <ClientFormModal
        open
        mode="edit"
        client={{
          email: 'label',
          enable: true,
          mieruUsername: '独立-user',
          mieruPassword: '独立-password',
          password: 'other-password',
        }}
        inbounds={[MIERU_INBOUND]}
        attachedIds={[9]}
        save={save}
        onOpenChange={() => {}}
      />,
    );
    credentialsTab();
    await waitFor(() => expect(credentialInput('mieru username').value).toBe('独立-user'));
    expect(credentialInput('mieru password').value).toBe('独立-password');
    fireEvent.change(credentialInput('Email'), { target: { value: 'renamed' } });
    fireEvent.click(await screen.findByRole('button', { name: /save/i }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0][0]).toMatchObject({
      email: 'renamed',
      mieruUsername: '独立-user',
      mieruPassword: '独立-password',
      password: 'other-password',
    });
    expect(save.mock.calls[0][1]).toMatchObject({ attach: [], detach: [], email: 'label' });
  });

  it('rejects a 65-byte native credential before client submission', async () => {
    const save = vi.fn().mockResolvedValue({ success: true });
    renderWithProviders(
      <ClientFormModal
        open
        mode="edit"
        client={{ email: 'label', enable: true, mieruUsername: 'user', mieruPassword: 'secret' }}
        inbounds={[MIERU_INBOUND]}
        attachedIds={[9]}
        save={save}
        onOpenChange={() => {}}
      />,
    );
    credentialsTab();
    fireEvent.change(credentialInput('mieru username'), {
      target: { value: '界'.repeat(21) + 'ab' },
    });
    fireEvent.click(await screen.findByRole('button', { name: /save/i }));
    await screen.findByText(
      'mieru credentials must be valid Unicode and no more than 64 UTF-8 bytes.',
    );
    expect(save).not.toHaveBeenCalled();
  });
});

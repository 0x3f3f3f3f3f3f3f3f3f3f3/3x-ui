import { describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import { EditorView } from '@codemirror/view';
import { useState } from 'react';

import OutboundFormModal from '@/pages/xray/outbounds/OutboundFormModal';
import { renderWithProviders } from './test-utils';
import { SSH_OUTBOUND_HOST_KEY, SSH_OUTBOUND_PRIVATE_KEY } from './ssh-outbound-fixture';

const settings = {
  address: 'ssh.example.net',
  port: 22,
  user: 'forward',
  privateKey: SSH_OUTBOUND_PRIVATE_KEY,
  hostKey: SSH_OUTBOUND_HOST_KEY,
};

function renderSSH(patch: Record<string, unknown> = {}) {
  const onConfirm = vi.fn();
  renderWithProviders(
    <OutboundFormModal
      open
      outbound={{ protocol: 'ssh', tag: 'ssh-exit', settings: { ...settings, ...patch } }}
      existingTags={[]}
      onClose={() => {}}
      onConfirm={onConfirm}
    />,
  );
  return onConfirm;
}

describe('SSH outbound editor', () => {
  it('keeps the key hidden until explicitly opened and preserves multiline edits', async () => {
    const onConfirm = renderSSH();
    expect(
      Array.from(document.querySelectorAll('input, textarea')).some((input) =>
        (input as HTMLInputElement).value.includes(SSH_OUTBOUND_PRIVATE_KEY),
      ),
    ).toBe(false);
    fireEvent.click(screen.getByRole('button', { name: 'Show / edit private key' }));
    const field = screen.getByLabelText('Private Key');
    expect((field as HTMLTextAreaElement).value).toBe(SSH_OUTBOUND_PRIVATE_KEY);
    const changed = SSH_OUTBOUND_PRIVATE_KEY + '\n';
    fireEvent.change(field, { target: { value: changed } });
    fireEvent.click(screen.getByRole('button', { name: 'Hide private key' }));
    fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }));
    await waitFor(() => expect(onConfirm).toHaveBeenCalledOnce());
    expect(onConfirm.mock.calls[0][0]).toEqual({
      tag: 'ssh-exit',
      protocol: 'ssh',
      settings: { ...settings, privateKey: changed },
    });
  });

  it('requires an explicit server host pin', async () => {
    const onConfirm = renderSSH({ hostKey: '' });
    fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }));
    await screen.findByText(
      'Enter an SSH public key, without authorized_keys options or certificates.',
    );
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it('rejects a routing tag that exceeds the backend UTF-8 byte limit', async () => {
    const onConfirm = renderSSH();
    fireEvent.change(screen.getByDisplayValue('ssh-exit'), { target: { value: '界'.repeat(43) } });
    fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }));
    await screen.findByText(
      'Use a nonempty tag of at most 128 UTF-8 bytes, without control characters or surrounding spaces.',
    );
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it('hides credentials when another outbound replaces the open editor', () => {
    function SwitchEditor() {
      const [tag, setTag] = useState('first');
      return (
        <>
          <button type="button" onClick={() => setTag('second')}>
            Switch outbound
          </button>
          <OutboundFormModal
            open
            existingTags={[]}
            onClose={() => {}}
            onConfirm={() => {}}
            outbound={{ protocol: 'ssh', tag, settings }}
          />
        </>
      );
    }
    renderWithProviders(<SwitchEditor />);
    fireEvent.click(screen.getByRole('button', { name: 'Show / edit private key' }));
    fireEvent.click(screen.getByRole('button', { name: 'Switch outbound' }));
    expect(screen.queryByRole('button', { name: 'Hide private key' })).toBeNull();
    expect(screen.getByRole('button', { name: 'Show / edit private key' })).toBeTruthy();
  });

  it('shows the capability requirement and omits inapplicable controls', () => {
    renderSSH();
    expect((screen.getByLabelText('Address') as HTMLInputElement).value).toBe(settings.address);
    expect((screen.getByLabelText('Port') as HTMLInputElement).value).toBe('22');
    expect(
      screen.getByText(
        'SSH upstreams support TCP forwarding only. Enable the core API before applying this outbound.',
      ),
    ).toBeTruthy();
    for (const label of ['Send Through', 'Target Strategy', 'Sockopts', 'Mux']) {
      expect(screen.queryByLabelText(label)).toBeNull();
    }
  });

  it('rejects unsupported JSON fields instead of discarding them on save', async () => {
    const onConfirm = renderSSH();
    fireEvent.click(screen.getByRole('tab', { name: 'JSON' }));
    const editor = document.querySelector('.cm-editor');
    if (!editor) throw new Error('JSON editor did not open');
    const view = EditorView.findFromDOM(editor as HTMLElement);
    if (!view) throw new Error('JSON editor has no mounted view');
    await act(async () => {
      view.dispatch({
        changes: {
          from: 0,
          to: view.state.doc.length,
          insert: JSON.stringify({
            protocol: 'ssh',
            tag: 'ssh-exit',
            settings,
            streamSettings: { sockopt: { dialerProxy: 'bypass' } },
          }),
        },
      });
    });
    fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }));
    await screen.findByText(
      'Invalid SSH outbound configuration. Check the required fields and remove unsupported settings.',
    );
    expect(onConfirm).not.toHaveBeenCalled();
  });
});

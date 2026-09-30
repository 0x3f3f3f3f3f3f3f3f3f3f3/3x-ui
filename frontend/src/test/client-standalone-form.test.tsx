import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { expect, it, vi } from 'vitest';

import { ThemeProvider } from '@/hooks/useTheme';
import ClientFormModal from '@/pages/clients/ClientFormModal';

it('creates an account with saved policy before any listener exists', async () => {
  const save = vi.fn().mockResolvedValue({ success: true });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <ThemeProvider>
      <QueryClientProvider client={qc}>
        <ClientFormModal
          open
          mode="add"
          client={null}
          inbounds={[]}
          save={save}
          onOpenChange={() => {}}
        />
      </QueryClientProvider>
    </ThemeProvider>,
  );
  fireEvent.change(screen.getByPlaceholderText('Email'), {
    target: { value: 'standalone-owner' },
  });
  fireEvent.change(screen.getByRole('textbox', { name: /^Billing multiplier/ }), {
    target: { value: '1.5' },
  });
  fireEvent.click(screen.getByRole('button', { name: /^create$/i }));
  await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
  expect(save.mock.calls[0][0]).toMatchObject({
    client: { email: 'standalone-owner', policy: { multiplier: '1.5' } },
    inboundIds: [],
  });
});

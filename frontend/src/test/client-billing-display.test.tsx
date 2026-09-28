import { expect, it } from 'vitest';
import { fireEvent, screen } from '@testing-library/react';
import ClientTrafficCell from '@/components/clients/ClientTrafficCell';
import { renderWithProviders } from './test-utils';

it('shows charged quota consumption while retaining raw directions and exact fractional bytes', async () => {
  renderWithProviders(
    <ClientTrafficCell
      up={10}
      down={20}
      total={100}
      billing={{
        up: '10',
        down: '20',
        billed: '99',
        remainder: 500,
        quota: '100',
        remaining: '1',
        unlimited: false,
        multiplier: '2',
        exhausted: true,
      }}
    />,
  );
  const bar = screen.getByRole('progressbar', { name: 'Billed usage: 99.5 B / 100 B' });
  expect(bar.getAttribute('aria-valuenow')).toBe('99.5');
  fireEvent.click(bar);
  await screen.findByText('0.5 B');
  expect(screen.getByText('10 B')).toBeTruthy();
  expect(screen.getByText('20 B')).toBeTruthy();
  expect(screen.getByText('2×')).toBeTruthy();
});

it('retains bytes beyond Number precision in the billed progress description', () => {
  renderWithProviders(
    <ClientTrafficCell
      billing={{
        up: '9007199254740993',
        down: '0',
        billed: '9007199254740993',
        remainder: 501,
        quota: '9007199254741993',
        remaining: '1000',
        unlimited: false,
        multiplier: '1',
        exhausted: false,
      }}
    />,
  );
  expect(
    screen
      .getByRole('progressbar', {
        name: 'Billed usage: 9007199254740993.501 B / 9007199254741993 B',
      })
      .getAttribute('aria-valuenow'),
  ).toBe('99.99');
});

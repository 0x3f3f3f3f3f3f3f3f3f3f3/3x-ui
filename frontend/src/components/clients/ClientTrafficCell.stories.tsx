import type { Meta, StoryObj } from '@storybook/react-vite';

import { ThemeProvider } from '@/hooks/useTheme';

import ClientTrafficCell from './ClientTrafficCell';

const GiB = 1024 ** 3;

const meta = {
  title: 'Clients/ClientTrafficCell',
  component: ClientTrafficCell,
  tags: ['autodocs'],
  decorators: [
    (Story) => (
      <ThemeProvider>
        <Story />
      </ThemeProvider>
    ),
  ],
  parameters: {
    layout: 'padded',
    docs: {
      description: {
        component:
          'Traffic usage cell for the clients table: used bytes, a color-coded progress bar, and the quota (or an infinity icon for unlimited clients), with an upload/download/remaining breakdown in a hover popover.',
      },
    },
  },
  argTypes: {
    up: { description: 'Uploaded bytes counted against the client.' },
    down: { description: 'Downloaded bytes counted against the client.' },
    total: { description: 'Traffic quota in bytes; 0 or less renders as unlimited.' },
    enabled: { description: 'Grays the bar out when the client is disabled.' },
    trafficDiff: {
      description:
        'Headroom in bytes below the quota at which the bar shifts from green to orange.',
    },
    compact: { description: 'Smaller bar and tighter layout for dense table rows.' },
    accounting: {
      description:
        'Confirmed lifetime and period accounting, with exact byte amounts and pending policy status.',
    },
  },
} satisfies Meta<typeof ClientTrafficCell>;

export default meta;

type Story = StoryObj<typeof meta>;

export const Default: Story = {
  args: {
    up: 6 * GiB,
    down: 35 * GiB,
    total: 100 * GiB,
    trafficDiff: 5 * GiB,
  },
};

export const Unlimited: Story = {
  args: {
    up: 87 * GiB,
    down: 940 * GiB,
    total: 0,
  },
};

export const Depleted: Story = {
  args: {
    up: 9 * GiB,
    down: 42 * GiB,
    total: 50 * GiB,
    trafficDiff: 5 * GiB,
  },
};

export const DisabledCompact: Story = {
  args: {
    up: 2 * GiB,
    down: 11 * GiB,
    total: 40 * GiB,
    enabled: false,
    compact: true,
  },
};

export const PendingAccounting: Story = {
  args: {
    accounting: {
      clientId: 'e18c9a96-71bf-48d4-933f-8b9a46d4290c',
      lifetime: {
        upload: '1073741824',
        download: '4294967296',
        billed: '10737418240.5',
        uncertain: '65536',
      },
      period: {
        upload: '268435456',
        download: '1073741824',
        billed: '2684354560.5',
        uncertain: '0',
      },
      quotaBytes: '10737418240',
      remaining: '8053063679.5',
      appliedVersion: '2',
      desiredVersion: '3',
      resetPending: true,
    },
  },
};

import { describe, it, expect } from 'vitest';

import { computeTrafficDisplay } from '@/lib/clients/traffic-display';
import { ClientTrafficSchema } from '@/schemas/client';

describe('computeTrafficDisplay', () => {
  const gb = 1024 * 1024 * 1024;

  it('returns 50% for half-used limited quota', () => {
    const d = computeTrafficDisplay(
      { up: 0.25 * gb, down: 0.25 * gb, total: gb, enabled: true, trafficDiff: 0 },
      false,
    );
    expect(d.percent).toBe(50);
    expect(d.isUnlimited).toBe(false);
    expect(d.remaining).toBe(0.5 * gb);
  });

  it('returns 100% bar for unlimited clients', () => {
    const d = computeTrafficDisplay(
      { up: 5 * gb, down: 2 * gb, total: 0, enabled: true, trafficDiff: 0 },
      false,
    );
    expect(d.percent).toBe(100);
    expect(d.isUnlimited).toBe(true);
    expect(d.strokeColor).toBe('#722ed1');
  });

  it('marks depleted clients with exception status', () => {
    const d = computeTrafficDisplay(
      { up: gb, down: 0, total: gb, enabled: true, trafficDiff: 0 },
      false,
    );
    expect(d.isDepleted).toBe(true);
    expect(d.status).toBe('exception');
    expect(d.percent).toBe(100);
  });

  it('uses gray stroke when client is disabled', () => {
    const d = computeTrafficDisplay(
      { up: 0.5 * gb, down: 0, total: gb, enabled: false, trafficDiff: 0 },
      false,
    );
    expect(d.strokeColor).toBe('#bcbcbc');
    expect(d.status).toBeUndefined();
  });

  it('uses warning color near traffic limit', () => {
    const diff = 0.1 * gb;
    const d = computeTrafficDisplay(
      { up: 0.95 * gb, down: 0, total: gb, enabled: true, trafficDiff: diff },
      false,
    );
    expect(d.strokeColor).toBe('#faad14');
  });

  it('uses confirmed billed usage from parsed statistics instead of legacy raw counters', () => {
    const traffic = ClientTrafficSchema.parse({
      up: 50,
      down: 50,
      total: 0,
      accounting: {
        clientId: 'e18c9a96-71bf-48d4-933f-8b9a46d4290c',
        lifetime: { upload: '25', download: '0', billed: '50', uncertain: '0' },
        period: { upload: '10', download: '0', billed: '20', uncertain: '0' },
        quotaBytes: '100',
        remaining: '80',
        appliedVersion: '2',
        desiredVersion: '2',
        resetPending: false,
        policyPending: false,
      },
    });
    const display = computeTrafficDisplay(
      {
        up: traffic.up!,
        down: traffic.down!,
        total: traffic.total!,
        enabled: true,
        trafficDiff: 0,
        accounting: traffic.accounting,
      },
      false,
    );
    expect(display.used).toBe(20);
    expect(display.percent).toBe(20);
    expect(display.remaining).toBe(80);
    expect(display.isUnlimited).toBe(false);
  });

  it('keeps positive exact remaining quota when large byte totals round to the same JS number', () => {
    const traffic = ClientTrafficSchema.parse({
      accounting: {
        clientId: 'e18c9a96-71bf-48d4-933f-8b9a46d4290c',
        lifetime: {
          upload: '9223372036854775799',
          download: '0',
          billed: '9223372036854775799.999999',
          uncertain: '7',
        },
        period: {
          upload: '9223372036854775796',
          download: '0',
          billed: '9223372036854775798.499999',
          uncertain: '0',
        },
        quotaBytes: '9223372036854775807',
        remaining: '8.500001',
        appliedVersion: '4',
        desiredVersion: '4',
        resetPending: false,
        policyPending: false,
      },
    });
    const display = computeTrafficDisplay(
      { up: 0, down: 0, total: 1, enabled: true, trafficDiff: 0, accounting: traffic.accounting },
      false,
    );
    expect(display.remaining).toBe(8.500001);
    expect(display.isDepleted).toBe(false);
    expect(display.status).toBeUndefined();
  });

  it('includes frozen uncertainty in quota consumption without changing raw traffic', () => {
    const traffic = ClientTrafficSchema.parse({
      accounting: {
        clientId: 'e18c9a96-71bf-48d4-933f-8b9a46d4290c',
        lifetime: { upload: '3', download: '0', billed: '1.5', uncertain: '7' },
        period: { upload: '3', download: '0', billed: '1.5', uncertain: '7' },
        quotaBytes: '10',
        remaining: '1.5',
        appliedVersion: '1',
        desiredVersion: '1',
        resetPending: false,
        policyPending: false,
      },
    });
    const display = computeTrafficDisplay(
      { up: 3, down: 0, total: 10, enabled: true, trafficDiff: 0, accounting: traffic.accounting },
      false,
    );
    expect(display.used).toBe(8.5);
    expect(display.percent).toBe(85);
    expect(display.remaining).toBe(1.5);
    expect(display.up).toBe(3);
  });
});

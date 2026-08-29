import { describe, expect, it } from 'vitest';

import type {
  OrderCheckListBody,
  OrderListBody,
} from 'gas-city-dashboard-shared/gc-supervisor';
import { cadenceLabel, mergeOrderRows, orderProvenance } from './orderReads';

function orderFixture(overrides: Record<string, unknown> = {}) {
  return {
    name: 'mayor-heartbeat',
    scoped_name: 'mayor-heartbeat',
    type: 'exec',
    trigger: 'cooldown',
    interval: '15m',
    enabled: true,
    capture_output: true,
    timeout_ms: 30_000,
    ...overrides,
  };
}

describe('cadenceLabel', () => {
  it('labels each trigger shape', () => {
    expect(cadenceLabel({ trigger: 'cooldown', interval: '15m' })).toBe('every 15m');
    expect(cadenceLabel({ trigger: 'cron', schedule: '0 3 * * *' })).toBe('0 3 * * *');
    expect(cadenceLabel({ trigger: 'event', on: 'bead.closed' })).toBe('on bead.closed');
    expect(cadenceLabel({ trigger: 'condition' })).toBe('condition');
    expect(cadenceLabel({ trigger: 'manual' })).toBe('manual');
  });

  it('falls back to the deprecated gate spelling', () => {
    expect(cadenceLabel({ gate: 'cooldown', interval: '15m' })).toBe('every 15m');
  });
});

describe('orderProvenance', () => {
  it('labels cache-resident pack orders with the pack name', () => {
    expect(
      orderProvenance({
        source: '/Users/x/.gc/cache/repos/abc123/gastown/orders/reaper.toml',
        formula_layer: '/Users/x/.gc/cache/repos/abc123/gastown/formulas',
      }),
    ).toBe('pack: gastown');
  });

  it('labels city orders as city-local', () => {
    expect(orderProvenance({ source: '/Users/x/gc/orders/mayor-heartbeat.toml' })).toBe(
      'city-local',
    );
  });
});

describe('mergeOrderRows', () => {
  it('merges checks by scoped_name and tolerates a missing check read', () => {
    const orders = {
      orders: [
        orderFixture(),
        orderFixture({
          name: 'gate-sweep',
          scoped_name: 'gate-sweep:rig:ascent',
          rig: 'ascent',
          enabled: false,
        }),
      ],
    } as unknown as OrderListBody;
    const checks = {
      checks: [
        {
          name: 'mayor-heartbeat',
          scoped_name: 'mayor-heartbeat',
          due: true,
          reason: 'cooldown elapsed',
          last_run: '2026-08-29T12:00:00Z',
          last_run_outcome: 'success',
        },
      ],
    } as unknown as OrderCheckListBody;

    const rows = mergeOrderRows(orders, checks);
    expect(rows).toHaveLength(2);
    const heartbeat = rows.find((row) => row.scopedName === 'mayor-heartbeat');
    expect(heartbeat?.due).toBe(true);
    expect(heartbeat?.lastRunOutcome).toBe('success');
    expect(heartbeat?.cadence).toBe('every 15m');
    const sweep = rows.find((row) => row.scopedName === 'gate-sweep:rig:ascent');
    expect(sweep?.rig).toBe('ascent');
    expect(sweep?.enabled).toBe(false);
    expect(sweep?.due).toBeUndefined();
    expect(sweep?.lastRun).toBeUndefined();
  });

  it('returns empty for an undefined list', () => {
    expect(mergeOrderRows(undefined, undefined)).toEqual([]);
  });
});

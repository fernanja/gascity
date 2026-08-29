import type {
  OrderCheckListBody,
  OrderCheckResponse,
  OrderListBody,
  OrderResponse,
} from 'gas-city-dashboard-shared/gc-supervisor';

/**
 * The Orders list view model: one row per order, merging the definition
 * (/orders) with the trigger evaluation (/orders/check) by scoped_name.
 * The check read is best-effort — a row whose check is missing simply has
 * no due/lastRun facts, it is never dropped.
 */
export interface OrderRow {
  name: string;
  scopedName: string;
  /** Empty string for city-scoped orders. */
  rig: string;
  description: string;
  /** 'formula' | 'exec'. */
  type: string;
  trigger: string;
  /** Human cadence: "every 5m", cron expr, "on bead.closed", "condition", "manual". */
  cadence: string;
  pool: string;
  enabled: boolean;
  /** 'city-local' or 'pack: <name>' — where the order file lives. */
  provenance: string;
  due: boolean | undefined;
  dueReason: string | undefined;
  lastRun: string | undefined;
  lastRunOutcome: string | undefined;
}

export function cadenceLabel(
  order: Pick<OrderResponse, 'trigger' | 'interval' | 'schedule' | 'on' | 'gate'>,
): string {
  const trigger = order.trigger || order.gate || '';
  switch (trigger) {
    case 'cooldown':
      return order.interval ? `every ${order.interval}` : 'cooldown';
    case 'cron':
      return order.schedule || 'cron';
    case 'event':
      return order.on ? `on ${order.on}` : 'event';
    case 'condition':
      return 'condition';
    case 'manual':
      return 'manual';
    default:
      return trigger || '·';
  }
}

/**
 * Provenance from the order's source path: a file under the pack cache or
 * the framework's bundled packs is "pack: <dir>", anything else (including
 * an absent source on older servers) reads as city-local.
 */
export function orderProvenance(order: Pick<OrderResponse, 'source' | 'formula_layer'>): string {
  const source = order.source ?? '';
  const isPack = source.includes('/.gc/cache/') || source.includes('/bootstrap/packs/');
  if (!isPack) return 'city-local';
  const layer = order.formula_layer ?? '';
  if (layer !== '') {
    const segments = layer.split('/').filter((segment) => segment !== '');
    // dir(formula_layer) is the pack root; its basename names the pack.
    if (segments.length >= 2) return `pack: ${segments[segments.length - 2]}`;
  }
  return 'pack';
}

export function mergeOrderRows(
  orders: OrderListBody | undefined,
  checks: OrderCheckListBody | undefined,
): OrderRow[] {
  const checkByScopedName = new Map<string, OrderCheckResponse>();
  for (const check of checks?.checks ?? []) {
    checkByScopedName.set(check.scoped_name, check);
  }
  return (orders?.orders ?? []).map((order) => {
    const check = checkByScopedName.get(order.scoped_name);
    return {
      name: order.name,
      scopedName: order.scoped_name,
      rig: order.rig ?? '',
      description: order.description ?? '',
      type: order.type,
      trigger: order.trigger || order.gate || '',
      cadence: cadenceLabel(order),
      pool: order.pool ?? '',
      enabled: order.enabled,
      provenance: orderProvenance(order),
      due: check?.due,
      dueReason: check?.reason,
      lastRun: check?.last_run,
      lastRunOutcome: check?.last_run_outcome,
    };
  });
}

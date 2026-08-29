import { GC_EVENT_PREFIX } from 'gas-city-dashboard-shared';
import type {
  OrderCheckListBody,
  OrderListBody,
} from 'gas-city-dashboard-shared/gc-supervisor';
import { useMemo } from 'react';
import { Link } from 'react-router-dom';
import { activeCityOrThrow, getActiveCity } from '../api/cityBase';
import { Button } from '../components/Button';
import { PageHeader } from '../components/PageHeader';
import { StatusBadge, type StatusTone } from '../components/StatusBadge';
import { useNow } from '../contexts/NowContext';
import { formatRelative } from '../hooks/time';
import { useCachedData } from '../hooks/useCachedData';
import { useGcEventRefresh } from '../hooks/useGcEvents';
import { supervisorApi } from '../supervisor/client';
import { orderDetailHref } from '../supervisor/orderHref';
import { mergeOrderRows, type OrderRow } from '../supervisor/orderReads';

interface OrdersReadModel {
  orders: OrderListBody;
  checks: OrderCheckListBody | undefined;
}

async function fetchOrdersModel(): Promise<OrdersReadModel> {
  const city = activeCityOrThrow('orders read');
  // The list is the load-bearing read; the check enrichment (due/last run)
  // is best-effort so a slow or failing trigger evaluation never blanks
  // the tab.
  const orders = await supervisorApi().listOrders(city, true);
  let checks: OrderCheckListBody | undefined;
  try {
    checks = await supervisorApi().ordersCheck(city);
  } catch {
    checks = undefined;
  }
  return { orders, checks };
}

export function OrdersPage() {
  const city = getActiveCity();
  const cityKey = city ?? 'no-city';
  const { data, loading, error, refresh } = useCachedData<OrdersReadModel>(
    `orders:${cityKey}`,
    fetchOrdersModel,
  );
  useGcEventRefresh([GC_EVENT_PREFIX.order], () => void refresh(), { coalesceMs: 10_000 });
  const now = useNow();

  const rows = useMemo(() => mergeOrderRows(data?.orders, data?.checks), [data]);
  const groups = useMemo(() => groupRowsByScope(rows), [rows]);

  const total = rows.length;
  const due = rows.filter((row) => row.due === true).length;
  const failed = rows.filter((row) => isFailureOutcome(row.lastRunOutcome)).length;
  const disabled = rows.filter((row) => !row.enabled).length;
  const checksMissing = data !== undefined && data.checks === undefined;

  const synopsis =
    data === undefined
      ? 'Scheduled and event-driven dispatch units registered with the controller.'
      : `${total} orders · ${due} due · ${failed} failed last run · ${disabled} disabled`;

  return (
    <section>
      <PageHeader
        title="Orders"
        synopsis={synopsis}
        meta={
          <>
            {error && (
              <span role="alert" className="text-warn normal-case tracking-normal">
                {error}
              </span>
            )}
            <Button size="sm" onClick={() => void refresh()} disabled={loading}>
              Refresh
            </Button>
          </>
        }
      />

      {checksMissing && (
        <p className="mb-6 text-label italic text-fg-faint">
          trigger evaluations unavailable · due and last-run columns are blank
        </p>
      )}

      {data === undefined ? (
        <p className="text-body text-fg-muted">{loading ? 'Loading orders.' : 'Orders unavailable.'}</p>
      ) : rows.length === 0 ? (
        <p className="text-body text-fg-muted">No orders are registered in this city.</p>
      ) : (
        groups.map((group) => (
          <section key={group.key} className="mb-10" aria-label={`${group.label} orders`}>
            <h2 className="mb-1 border-b border-rule pb-2 text-label uppercase tracking-wider text-fg-faint">
              {group.label}
              <span className="ml-2 text-fg-muted tnum">{group.rows.length}</span>
            </h2>
            <ol className="m-0 list-none divide-y divide-rule p-0">
              {group.rows.map((row) => (
                <OrderRowItem key={row.scopedName} row={row} now={now} />
              ))}
            </ol>
          </section>
        ))
      )}
    </section>
  );
}

interface OrderGroup {
  key: string;
  label: string;
  rows: OrderRow[];
}

function groupRowsByScope(rows: readonly OrderRow[]): OrderGroup[] {
  const byRig = new Map<string, OrderRow[]>();
  for (const row of rows) {
    const key = row.rig || '';
    const bucket = byRig.get(key);
    if (bucket === undefined) byRig.set(key, [row]);
    else bucket.push(row);
  }
  const groups: OrderGroup[] = [];
  const city = byRig.get('');
  if (city !== undefined) groups.push({ key: 'city', label: 'City', rows: sortRows(city) });
  for (const rig of [...byRig.keys()].filter((key) => key !== '').sort()) {
    groups.push({ key: `rig:${rig}`, label: rig, rows: sortRows(byRig.get(rig) ?? []) });
  }
  return groups;
}

function sortRows(rows: OrderRow[]): OrderRow[] {
  // Enabled first, then alphabetical — disabled orders sink but stay visible.
  return [...rows].sort((a, b) => {
    if (a.enabled !== b.enabled) return a.enabled ? -1 : 1;
    return a.name.localeCompare(b.name);
  });
}

function isFailureOutcome(outcome: string | undefined): boolean {
  return outcome !== undefined && outcome.toLowerCase().includes('fail');
}

function outcomeTone(outcome: string): StatusTone {
  const lowered = outcome.toLowerCase();
  if (lowered.includes('fail') || lowered.includes('error')) return 'warn';
  if (lowered.includes('success') || lowered.includes('complete') || lowered === 'ok') return 'ok';
  return 'neutral';
}

function OrderRowItem({ row, now }: { row: OrderRow; now: number }) {
  return (
    <li className={`py-3 ${row.enabled ? '' : 'opacity-60'}`}>
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <Link
          to={orderDetailHref(row.scopedName)}
          className="focus-mark text-body font-semibold text-fg no-underline hover:underline"
        >
          {row.name}
        </Link>
        {!row.enabled && (
          <span className="text-label uppercase tracking-wider text-fg-faint">disabled</span>
        )}
        {row.due === true && (
          <span className="text-label uppercase tracking-wider text-accent" title={row.dueReason}>
            due
          </span>
        )}
        <span className="ml-auto flex flex-wrap items-baseline gap-x-3 text-label text-fg-muted">
          <span className="uppercase tracking-wider">{row.type}</span>
          <span className="tnum">{row.cadence}</span>
          {row.pool && <span>→ {row.pool}</span>}
          <span className="tnum" title={row.lastRun}>
            {row.lastRun !== undefined ? `ran ${formatRelative(row.lastRun, now)} ago` : 'never ran'}
          </span>
          {row.lastRunOutcome !== undefined && (
            <StatusBadge tone={outcomeTone(row.lastRunOutcome)} label={row.lastRunOutcome} />
          )}
        </span>
      </div>
      {row.description && (
        <p className="mt-0.5 max-w-prose truncate text-body text-fg-muted" title={row.description}>
          {row.description}
        </p>
      )}
      <p className="mt-0.5 text-label text-fg-faint">{row.provenance}</p>
    </li>
  );
}

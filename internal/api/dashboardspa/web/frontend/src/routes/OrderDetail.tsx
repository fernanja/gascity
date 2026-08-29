import { GC_EVENT_PREFIX } from 'gas-city-dashboard-shared';
import type {
  OrderFileEntry,
  OrderFilesBody,
  OrderHistoryEntry,
  OrderHistoryListBody,
  OrderResponse,
} from 'gas-city-dashboard-shared/gc-supervisor';
import { useCallback, useMemo, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { activeCityOrThrow, getActiveCity } from '../api/cityBase';
import { Button } from '../components/Button';
import { PageHeader } from '../components/PageHeader';
import { StatusBadge, type StatusTone } from '../components/StatusBadge';
import { Table, type TableColumn } from '../components/Table';
import { TranscriptBox } from '../components/TranscriptBox';
import { useNow } from '../contexts/NowContext';
import { formatRelative } from '../hooks/time';
import { useCachedData } from '../hooks/useCachedData';
import { useGcEventRefresh } from '../hooks/useGcEvents';
import { supervisorApi } from '../supervisor/client';
import { cadenceLabel, orderProvenance } from '../supervisor/orderReads';

const HISTORY_PAGE_SIZE = 20;

type OrderTab = 'files' | 'runs';

export function OrderDetailPage() {
  const { scopedName: rawScopedName } = useParams();
  const scopedName = rawScopedName ?? '';
  const city = getActiveCity();
  const cityKey = city ?? 'no-city';

  const ordersState = useCachedData(`orders:detail:${cityKey}`, () =>
    supervisorApi().listOrders(activeCityOrThrow('order detail read'), true),
  );
  const filesState = useCachedData<OrderFilesBody>(`orders:files:${cityKey}:${scopedName}`, () =>
    supervisorApi().orderFiles(activeCityOrThrow('order files read'), scopedName),
  );

  const order = useMemo(
    () => (ordersState.data?.orders ?? []).find((entry) => entry.scoped_name === scopedName),
    [ordersState.data, scopedName],
  );

  const [tab, setTab] = useState<OrderTab>('files');

  return (
    <section>
      <PageHeader
        title={order?.name ?? scopedName}
        synopsis={order?.description || 'Order detail.'}
        meta={
          <>
            <Link to="/orders" className="focus-mark text-fg-muted no-underline hover:text-fg">
              Orders
            </Link>
            {ordersState.error && (
              <span role="alert" className="text-warn normal-case tracking-normal">
                {ordersState.error}
              </span>
            )}
          </>
        }
      />

      {order === undefined ? (
        <p className="text-body text-fg-muted">
          {ordersState.loading ? 'Loading order.' : `Order ${scopedName} was not found.`}
        </p>
      ) : (
        <>
          <OrderMetadata order={order} />

          <div
            className="mt-8 flex items-baseline gap-4 text-label"
            role="tablist"
            aria-label="Order views"
          >
            <OrderTabButton id="files" active={tab === 'files'} onSelect={setTab}>
              Files
            </OrderTabButton>
            <OrderTabButton id="runs" active={tab === 'runs'} onSelect={setTab}>
              Runs
            </OrderTabButton>
          </div>

          <div
            id={`order-tab-panel-${tab}`}
            role="tabpanel"
            aria-labelledby={`order-tab-${tab}`}
            className="pt-5"
          >
            {tab === 'files' ? (
              <OrderFilesPanel
                files={filesState.data}
                loading={filesState.loading}
                error={filesState.error}
              />
            ) : (
              <OrderRunsPanel scopedName={scopedName} cityKey={cityKey} />
            )}
          </div>
        </>
      )}
    </section>
  );
}

function OrderTabButton({
  id,
  active,
  onSelect,
  children,
}: {
  id: OrderTab;
  active: boolean;
  onSelect: (tab: OrderTab) => void;
  children: string;
}) {
  return (
    <button
      id={`order-tab-${id}`}
      type="button"
      role="tab"
      aria-selected={active}
      aria-controls={`order-tab-panel-${id}`}
      onClick={() => onSelect(id)}
      className={`focus-mark rounded-sm px-0.5 uppercase tracking-wider ${
        active
          ? 'font-semibold text-fg underline decoration-fg underline-offset-4'
          : 'text-fg-muted hover:text-fg'
      }`}
    >
      {children}
    </button>
  );
}

function OrderMetadata({ order }: { order: OrderResponse }) {
  return (
    <dl className="grid gap-x-8 gap-y-3 sm:grid-cols-2 lg:grid-cols-4">
      <Meta label="Type" value={order.type} />
      <Meta label="Trigger" value={cadenceLabel(order)} />
      {order.pool && <Meta label="Pool" value={order.pool} />}
      {order.formula && <Meta label="Formula" value={order.formula} />}
      <Meta label="Timeout" value={order.timeout || `${order.timeout_ms}ms`} />
      <Meta label="Enabled" value={order.enabled ? 'yes' : 'no'} />
      <Meta label="Scope" value={order.rig ? `rig:${order.rig}` : 'city'} />
      <Meta label="Source" value={orderProvenance(order)} />
    </dl>
  );
}

function Meta({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-label uppercase tracking-wider text-fg-faint">{label}</dt>
      <dd className="text-body text-fg break-all tnum">{value}</dd>
    </div>
  );
}

function OrderFilesPanel({
  files,
  loading,
  error,
}: {
  files: OrderFilesBody | undefined;
  loading: boolean;
  error: string | null;
}) {
  const entries = useMemo(() => files?.files ?? [], [files]);
  const [selectedPath, setSelectedPath] = useState<string | null>(null);
  const selected =
    entries.find((entry) => entry.path === selectedPath && entry.error === undefined) ??
    entries.find((entry) => entry.error === undefined);

  if (files === undefined) {
    return (
      <p className="text-body text-fg-muted">
        {loading ? 'Loading order files.' : (error ?? 'Order files unavailable.')}
      </p>
    );
  }
  if (entries.length === 0) {
    return <p className="text-body text-fg-muted">This order references no readable files.</p>;
  }
  return (
    <div className="grid gap-6 lg:grid-cols-[minmax(16rem,1fr)_minmax(0,3fr)]">
      <ol className="m-0 list-none divide-y divide-rule p-0" aria-label="Order files">
        {entries.map((entry) => (
          <li key={`${entry.role}:${entry.path || entry.ref}`}>
            {entry.error !== undefined ? (
              <div className="py-2 opacity-60">
                <FileLabel entry={entry} />
                <p className="text-label italic text-fg-faint">{entry.error}</p>
              </div>
            ) : (
              <button
                type="button"
                onClick={() => setSelectedPath(entry.path)}
                aria-current={selected?.path === entry.path}
                className={`focus-mark block w-full py-2 text-left ${
                  selected?.path === entry.path ? '' : 'opacity-70 hover:opacity-100'
                }`}
              >
                <FileLabel entry={entry} emphasized={selected?.path === entry.path} />
              </button>
            )}
          </li>
        ))}
      </ol>
      {selected !== undefined && (
        <div className="min-w-0">
          <div className="mb-2 flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
            <span className="break-all text-label text-fg-muted">{selected.path}</span>
            <span className="text-label uppercase tracking-wider text-fg-faint">
              {selected.language} · {formatBytes(selected.size_bytes)}
            </span>
          </div>
          {selected.truncated === true && (
            <p className="mb-2 text-label italic text-warn">
              content truncated — showing the first 256 KB
            </p>
          )}
          <pre className="max-h-[36rem] overflow-auto border-y border-rule py-3 text-body leading-relaxed">
            {selected.content ?? ''}
          </pre>
        </div>
      )}
    </div>
  );
}

function FileLabel({ entry, emphasized }: { entry: OrderFileEntry; emphasized?: boolean }) {
  const basename = entry.path !== '' ? (entry.path.split('/').pop() ?? entry.ref) : entry.ref;
  return (
    <span className="block">
      <span className="mr-2 text-label uppercase tracking-wider text-fg-faint">{entry.role}</span>
      <span className={`text-body ${emphasized ? 'font-semibold text-fg' : 'text-fg'}`}>
        {basename}
      </span>
      {entry.ref !== entry.path && entry.ref !== basename && (
        <span className="block truncate text-label text-fg-faint" title={entry.ref}>
          {entry.ref}
        </span>
      )}
    </span>
  );
}

function formatBytes(size: number): string {
  if (size >= 1024 * 1024) return `${(size / (1024 * 1024)).toFixed(1)} MB`;
  if (size >= 1024) return `${(size / 1024).toFixed(1)} KB`;
  return `${size} B`;
}

function OrderRunsPanel({ scopedName, cityKey }: { scopedName: string; cityKey: string }) {
  const [pages, setPages] = useState<OrderHistoryEntry[][]>([]);
  const [loadingMore, setLoadingMore] = useState(false);
  const historyState = useCachedData<OrderHistoryListBody>(
    `orders:history:${cityKey}:${scopedName}`,
    () =>
      supervisorApi().orderHistory(
        activeCityOrThrow('order history read'),
        scopedName,
        HISTORY_PAGE_SIZE,
      ),
  );
  useGcEventRefresh([GC_EVENT_PREFIX.order], () => void historyState.refresh(), {
    coalesceMs: 10_000,
  });
  const now = useNow();

  const entries = useMemo(
    () => [...(historyState.data?.entries ?? []), ...pages.flat()],
    [historyState.data, pages],
  );

  const loadMore = useCallback(async () => {
    const oldest = entries.at(-1);
    if (oldest === undefined) return;
    setLoadingMore(true);
    try {
      const page = await supervisorApi().orderHistory(
        activeCityOrThrow('order history read'),
        scopedName,
        HISTORY_PAGE_SIZE,
        oldest.created_at,
      );
      setPages((previous) => [...previous, page.entries ?? []]);
    } finally {
      setLoadingMore(false);
    }
  }, [entries, scopedName]);

  const [selectedRun, setSelectedRun] = useState<OrderHistoryEntry | null>(null);
  const [output, setOutput] = useState<{ beadId: string; text: string } | 'loading' | null>(null);

  const openRun = useCallback((entry: OrderHistoryEntry) => {
    setSelectedRun(entry);
    setOutput('loading');
    void supervisorApi()
      // store_ref threads through so split-city bead IDs resolve in the
      // store that actually holds the tracking bead.
      .orderHistoryDetail(activeCityOrThrow('order run read'), entry.bead_id, entry.store_ref)
      .then(
        (detail) => setOutput({ beadId: detail.bead_id, text: detail.output }),
        () => setOutput({ beadId: entry.bead_id, text: '(output unavailable)' }),
      );
  }, []);

  const columns: TableColumn<OrderHistoryEntry>[] = [
    {
      key: 'created_at',
      label: 'When',
      sortable: true,
      sortValue: (row) => row.created_at,
      render: (row) => (
        <span title={row.created_at}>{formatRelative(row.created_at, now)} ago</span>
      ),
    },
    {
      key: 'duration',
      label: 'Duration',
      align: 'right',
      render: (row) => (row.duration_ms !== undefined ? `${row.duration_ms} ms` : '·'),
    },
    {
      key: 'exit',
      label: 'Exit',
      render: (row) => <ExitBadge entry={row} />,
    },
    {
      key: 'output',
      label: 'Output',
      render: (row) => (row.has_output ? 'view' : '·'),
    },
  ];

  return (
    <div>
      <Table
        columns={columns}
        rows={entries}
        rowKey={(row) => row.bead_id}
        onRowClick={openRun}
        empty={historyState.loading ? 'Loading run history.' : 'No recorded runs for this order.'}
      />
      {entries.length > 0 && entries.length % HISTORY_PAGE_SIZE === 0 && (
        <div className="mt-4">
          <Button size="sm" onClick={() => void loadMore()} disabled={loadingMore}>
            {loadingMore ? 'Loading…' : 'Load more'}
          </Button>
        </div>
      )}
      {selectedRun !== null && (
        <div className="mt-8">
          <h3 className="mb-2 text-label uppercase tracking-wider text-fg-faint">
            run output · <span className="normal-case tracking-normal">{selectedRun.bead_id}</span>
          </h3>
          {output === 'loading' ? (
            <p className="text-body text-fg-muted">Loading output.</p>
          ) : output !== null ? (
            <TranscriptBox caption={selectedRun.created_at}>
              <pre className="whitespace-pre-wrap text-body leading-relaxed">
                {output.text || '(no captured output)'}
              </pre>
            </TranscriptBox>
          ) : null}
        </div>
      )}
    </div>
  );
}

function ExitBadge({ entry }: { entry: OrderHistoryEntry }) {
  if (entry.error !== undefined && entry.error !== '') {
    return <StatusBadge tone="warn" label="error" title={entry.error} />;
  }
  if (entry.exit_code === undefined) {
    return <StatusBadge tone="neutral" label="·" />;
  }
  const tone: StatusTone = entry.exit_code === '0' ? 'ok' : 'warn';
  return <StatusBadge tone={tone} label={`exit ${entry.exit_code}`} />;
}

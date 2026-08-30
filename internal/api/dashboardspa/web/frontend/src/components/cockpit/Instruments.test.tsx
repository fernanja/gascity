import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import {
  ActivityTrace,
  BarList,
  TrendLine,
  Gauge,
  Odometer,
  PipelineBar,
  RunRings,
  StatTile,
  StatusLamps,
} from './Instruments';

describe('cockpit instruments', () => {
  afterEach(() => cleanup());

  it('exposes the odometer reading in the accessibility tree', () => {
    render(<Odometer label="model calls today" value={42} note="$1.20 estimated" />);
    expect(screen.getByRole('status', { name: 'model calls today: 42' })).toBeTruthy();
  });

  it('announces a stat tile reading and falls back to unavailable without inventing zero', () => {
    const { rerender } = render(<StatTile label="tokens in" value="300K" note="24h estimate" />);
    const tile = screen.getByRole('status', { name: 'tokens in: 300K' });
    expect(tile.textContent).toContain('300K');
    expect(tile.textContent).toContain('24h estimate');

    rerender(<StatTile label="tokens in" value={null} />);
    const empty = screen.getByRole('status', { name: 'tokens in: unavailable' });
    expect(empty.textContent).toContain('—');
  });

  it('keeps the full gauge scale inside its SVG viewport', () => {
    const { container } = render(
      <MemoryRouter future={{ v7_relativeSplatPath: true, v7_startTransition: true }}>
        <Gauge label="active sessions" value={2} max={10} formatted="2" href="/agents" />
      </MemoryRouter>,
    );
    const svg = container.querySelector('svg');
    expect(svg).not.toBeNull();
    const [, , width = 0, height = 0] = (svg?.getAttribute('viewBox') ?? '').split(' ').map(Number);
    for (const line of container.querySelectorAll('svg line')) {
      for (const [axis, bound] of [
        ['x1', width],
        ['x2', width],
        ['y1', height],
        ['y2', height],
      ] as const) {
        const coordinate = Number(line.getAttribute(axis));
        expect(coordinate, `${axis} should be inside the gauge viewport`).toBeGreaterThanOrEqual(0);
        expect(coordinate, `${axis} should be inside the gauge viewport`).toBeLessThanOrEqual(
          bound,
        );
      }
    }
  });

  it('keeps one comfortably-sized pipeline control per state', () => {
    render(
      <MemoryRouter future={{ v7_relativeSplatPath: true, v7_startTransition: true }}>
        <PipelineBar
          segments={[
            { key: 'pending', label: 'queued', count: 2, href: '/runs' },
            { key: 'active', label: 'running', count: 1, href: '/runs' },
          ]}
        />
      </MemoryRouter>,
    );
    expect(screen.getAllByTestId('pipeline-track-segment')).toHaveLength(2);
    expect(screen.getAllByRole('link')).toHaveLength(2);
    expect(screen.getByRole('link', { name: 'queued: 2' }).className).toContain('min-h-6');
  });

  it('announces unavailable traces and pipeline counts without inventing zero readings', () => {
    render(
      <MemoryRouter future={{ v7_relativeSplatPath: true, v7_startTransition: true }}>
        <ActivityTrace samples={[]} available={false} note="usage unavailable" />
        <PipelineBar
          available={false}
          segments={[
            { key: 'pending', label: 'queued', count: 0, href: '/runs' },
            { key: 'active', label: 'running', count: 0, href: '/runs' },
          ]}
        />
      </MemoryRouter>,
    );

    expect(
      screen.getByRole('figure', { name: /recent model activity: unavailable/i }),
    ).toBeTruthy();
    expect(screen.getByRole('link', { name: 'queued: unavailable' }).textContent).toContain('—');
  });

  it('keeps a partial activity reading available while announcing its provenance', () => {
    render(<ActivityTrace samples={[2]} available note="usage estimate is partial" />);

    expect(
      screen.getByRole('figure', {
        name: /recent model activity: 2 invocations in the current window; usage estimate is partial/i,
      }),
    ).toBeTruthy();
  });

  it('announces variable stage totals and retries', () => {
    render(
      <MemoryRouter future={{ v7_relativeSplatPath: true, v7_startTransition: true }}>
        <RunRings
          runs={[
            {
              id: 'r1',
              label: 'Deploy',
              stage: 7,
              totalStages: 7,
              stageWord: 'publish',
              attempt: 3,
              href: '/runs/r1',
            },
          ]}
        />
      </MemoryRouter>,
    );
    expect(
      screen.getByRole('link', { name: 'Deploy: stage 7 of 7, retry attempt 3' }),
    ).toBeTruthy();
  });

  it('announces lamp health without relying on color', () => {
    render(
      <MemoryRouter future={{ v7_relativeSplatPath: true, v7_startTransition: true }}>
        <StatusLamps
          lamps={[
            {
              key: 'store',
              label: 'store',
              value: 'maintenance overdue',
              state: 'warning',
              href: '/health',
            },
            {
              key: 'feed',
              label: 'live feed',
              value: 'disconnected',
              state: 'unknown',
              href: '/activity',
            },
          ]}
        />
      </MemoryRouter>,
    );
    expect(screen.getByRole('link', { name: 'store: warning, maintenance overdue' })).toBeTruthy();
    expect(screen.getByRole('link', { name: 'live feed: unknown, disconnected' })).toBeTruthy();
  });
});

describe('bar list', () => {
  afterEach(() => cleanup());

  it('renders one labelled row per item with proportional widths', () => {
    const { container } = render(
      <BarList
        items={[
          { key: 'build-basic', label: 'build-basic', value: 40 },
          { key: 'do-work', label: 'do-work', value: 20 },
        ]}
      />,
    );
    expect(screen.getByRole('status', { name: 'build-basic: 40' })).toBeTruthy();
    expect(screen.getByRole('status', { name: 'do-work: 20' })).toBeTruthy();
    const segments = container.querySelectorAll('[data-testid="bar-list-segment"]');
    expect(segments).toHaveLength(2);
    expect((segments[0] as HTMLElement).style.width).toBe('100%');
    expect((segments[1] as HTMLElement).style.width).toBe('50%');
  });

  it('renders outcome segments and drops zero-width ones', () => {
    const { container } = render(
      <BarList
        items={[
          {
            key: 'fix-loop',
            label: 'fix-loop',
            value: 10,
            segments: [
              { key: 'passed', value: 8, className: 'bg-ok/70' },
              { key: 'failed', value: 2, className: 'bg-warn/70' },
              { key: 'skipped', value: 0, className: 'bg-fg/25' },
            ],
          },
        ]}
      />,
    );
    const segments = container.querySelectorAll('[data-testid="bar-list-segment"]');
    expect(segments).toHaveLength(2);
  });

  it('shows the empty label when there is nothing to chart', () => {
    render(<BarList items={[]} emptyLabel="no runs completed in this window" />);
    expect(screen.getByText('no runs completed in this window')).toBeTruthy();
  });
});

describe('trend line', () => {
  afterEach(() => cleanup());

  it('exposes the series total in the accessibility tree', () => {
    render(<TrendLine label="runs completed" samples={[1, 0, 3]} caption="per hour · 72h" />);
    expect(screen.getByRole('figure', { name: 'runs completed: 4 over the window' })).toBeTruthy();
    expect(screen.getByText('per hour · 72h')).toBeTruthy();
  });

  it('renders a polyline spanning the series', () => {
    const { container } = render(<TrendLine label="beads closed" samples={[2, 2]} />);
    const polyline = container.querySelector('polyline');
    expect(polyline?.getAttribute('points')).toBe('0,4 100,4');
  });
});

import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { BenchmarkRunResult } from '../src/BenchmarkRunResult';
import type { BenchmarkRun } from '../src/benchmarkApi';

afterEach(cleanup);

const completed: BenchmarkRun = {
  id: 'run-1', status: 'succeeded', createdAt: '2026-10-02T00:00:00Z',
  completedAt: '2026-10-02T00:01:00Z', outputTruncated: false,
};
const metrics = [
  { name: 'tps', unit: 'tx/s', value: 1234.5 },
  { name: 'latency_avg', unit: 'ms', value: 0 },
];
// Separators depend on the runtime locale; compare the rendered digits only.
const digits = (expected: string) => (text: string) => text.replace(/\D/g, '') === expected;
const card = (run: BenchmarkRun | null) => (
  <BenchmarkRunResult id={run?.id ?? 'run-1'} run={run} feedback={null} onRetry={() => {}} />
);

describe('benchmark result metrics and logs', () => {
  it('shows metrics and effective snapshots while raw logs start collapsed', async () => {
    render(card({ ...completed, driver: 'custom-driver', runnerImage: 'registry/custom:2', jobName: 'job-1',
      options: { workers: 4, enabled: false, count: 0, mode: { access: 'read' } },
      metrics, output: 'native tool output\nTPS: 1234.5',
    }));
    expect(screen.getByText('custom-driver')).toBeTruthy();
    expect(screen.getByText('registry/custom:2')).toBeTruthy();
    expect(screen.queryByText('job-1')).toBeNull();
    const options = within(screen.getByRole('region', { name: 'Effective benchmark options' }));
    expect(options.getByText('false')).toBeTruthy();
    expect(options.getByText('0')).toBeTruthy();
    expect(options.getByText('{"access":"read"}')).toBeTruthy();
    const table = within(screen.getByRole('table', { name: 'Benchmark metrics' }));
    expect(table.getByRole('columnheader', { name: 'Metric' })).toBeTruthy();
    expect(table.getByRole('rowheader', { name: 'tps' })).toBeTruthy();
    expect(table.getByRole('cell', { name: digits('12345') })).toBeTruthy();
    expect(table.getByRole('cell', { name: '0' })).toBeTruthy();
    expect(table.getByRole('cell', { name: 'ms' })).toBeTruthy();
    const logs = screen.getByRole('button', { name: 'Raw logs' });
    expect(logs.getAttribute('aria-expanded')).toBe('false');
    expect(screen.queryByRole('region', { name: 'Raw logs' })).toBeNull();
    fireEvent.click(logs);
    await waitFor(() => expect(screen.getByRole('region', { name: 'Raw logs' })).toBeTruthy());
    expect(screen.getByLabelText('Benchmark output').textContent).toBe('native tool output\nTPS: 1234.5');
    fireEvent.click(logs);
    await waitFor(() => expect(screen.queryByRole('region', { name: 'Raw logs' })).toBeNull());
    expect(screen.getByRole('table', { name: 'Benchmark metrics' })).toBeTruthy();
  });

  it('shows unavailable metrics for older completed runs and still allows their logs to be opened', () => {
    render(card({ ...completed, output: 'legacy output' }));
    expect(screen.getByRole('status').textContent).toContain('Succeeded');
    expect(screen.getByText('Structured metrics unavailable.')).toBeTruthy();
    expect(screen.queryByRole('table')).toBeNull();
    expect(screen.queryByRole('region', { name: 'Effective benchmark options' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Raw logs' }));
    expect(screen.getByLabelText('Benchmark output').textContent).toBe('legacy output');
  });

  it('keeps failure messages visible when metrics and truncated logs are available', () => {
    render(card({ ...completed, status: 'failed', error: 'cleanup failed', metrics,
      output: 'saved partial output', outputTruncated: true,
    }));
    expect(screen.getByRole('alert').textContent).toContain('cleanup failed');
    expect(screen.getByRole('status').textContent).toContain('Failed');
    expect(screen.getByRole('table', { name: 'Benchmark metrics' })).toBeTruthy();
    const logs = screen.getByRole('button', { name: 'Raw logs Truncated' });
    expect(logs.getAttribute('aria-expanded')).toBe('false');
    fireEvent.click(logs);
    expect(screen.getByText('Output was truncated by the backend.')).toBeTruthy();
    expect(screen.getByLabelText('Benchmark output').textContent).toBe('saved partial output');
  });

  it('shows metrics even when no logs were captured and handles an empty options snapshot', () => {
    render(card({ ...completed, options: {}, metrics }));
    expect(screen.getByText('No driver options recorded.')).toBeTruthy();
    expect(screen.getByRole('table', { name: 'Benchmark metrics' })).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Raw logs' }));
    expect(screen.getByText('No output was captured.')).toBeTruthy();
  });

  it('waits for a terminal status before showing metrics availability or logs', () => {
    const view = render(card(null));
    expect(screen.queryByText('Structured metrics unavailable.')).toBeNull();
    expect(screen.queryByRole('button', { name: /Raw logs/ })).toBeNull();
    view.rerender(card({ ...completed, status: 'running', completedAt: undefined, driver: 'pgbench', metrics }));
    expect(screen.getByText('pgbench')).toBeTruthy();
    expect(screen.queryByRole('table')).toBeNull();
    expect(screen.queryByText('Structured metrics unavailable.')).toBeNull();
    expect(screen.queryByRole('button', { name: /Raw logs/ })).toBeNull();
    view.rerender(card(completed));
    expect(screen.getByText('Structured metrics unavailable.')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Raw logs' })).toBeTruthy();
  });

  it('rounds raw runner precision without collapsing small values to zero', () => {
    render(card({ ...completed, metrics: [
      { name: 'tps', unit: 'tx/s', value: 1234.567891 },
      { name: 'latency_avg', unit: 'ms', value: 0.004321 },
    ] }));
    const table = within(screen.getByRole('table', { name: 'Benchmark metrics' }));
    expect(table.getByRole('cell', { name: digits('123457') })).toBeTruthy();
    expect(table.getByRole('cell', { name: digits('000432') })).toBeTruthy();
  });

  it('keeps log expansion independent for cards displayed side by side', () => {
    render(<>{card({ ...completed, output: 'first logs', metrics })}
      {card({ ...completed, id: 'run-2', output: 'second logs',
        metrics: [{ name: 'operations', unit: 'op/s', value: 42 }],
      })}</>);
    const [first, second] = screen.getAllByRole('region', { name: 'Benchmark run result' }).map(element => within(element));
    const firstLogs = first.getByRole('button', { name: 'Raw logs' });
    const secondLogs = second.getByRole('button', { name: 'Raw logs' });
    expect(firstLogs.getAttribute('aria-controls')).not.toBe(secondLogs.getAttribute('aria-controls'));
    fireEvent.click(firstLogs);
    expect(firstLogs.getAttribute('aria-expanded')).toBe('true');
    expect(secondLogs.getAttribute('aria-expanded')).toBe('false');
    expect(first.getByRole('rowheader', { name: 'tps' })).toBeTruthy();
    expect(second.getByRole('rowheader', { name: 'operations' })).toBeTruthy();
  });
});

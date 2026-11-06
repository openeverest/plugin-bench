import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import type { PluginApi } from '@openeverest/plugin-sdk';
import { BenchmarkRunHistory } from '../src/BenchmarkRunHistory';
import { BenchmarkTab } from '../src/BenchmarkTab';

afterEach(() => { cleanup(); vi.useRealTimers(); });

const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const target = { k8sCluster: 'main', namespace: 'default', instance: 'pg-1' };
const summary = (id: string, extra: Record<string, unknown> = {}) => ({
  id, status: 'succeeded', createdAt: '2026-11-01T10:00:00Z', completedAt: '2026-11-01T10:01:15Z',
  outputTruncated: false, driver: 'pgbench', options: {}, ...extra,
});
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(done => { resolve = done; });
  return { promise, resolve };
}

const renderHistory = async (fetch: PluginApi['fetch'], props: Partial<Parameters<typeof BenchmarkRunHistory>[0]> = {}) => {
  let view!: ReturnType<typeof render>;
  const element = (overrides: typeof props = {}) => (
    <BenchmarkRunHistory pluginFetch={fetch} target={target} refreshKey={0} persistentHistory={true}
      shownRunIds={[]} onOpen={() => {}} {...props} {...overrides} />
  );
  await act(async () => { view = render(element()); });
  return { view, rerender: (overrides: typeof props) => act(async () => { view.rerender(element(overrides)); }) };
};
const rowFor = (id: string) => screen.getByRole('button', { name: new RegExp(id) }).closest('tr')!;

describe('benchmark run history', () => {
  it('summarizes each run with its tool, status, duration, and results', async () => {
    const fetch = vi.fn<PluginApi['fetch']>(async () => response({ runs: [
      summary('fast', { metrics: [{ name: 'tps', unit: 'tx/s', value: 12 }, { name: 'latency_avg', unit: 'ms', value: 0.5 }] }),
      summary('broken', { status: 'failed', driver: 'sysbench', error: 'benchmark execution failed', completedAt: '2026-11-01T10:00:20Z' }),
      summary('active', { status: 'running', completedAt: undefined, driver: 'future-tool' }),
    ] }));
    await renderHistory(fetch);
    const cells = (id: string) => within(rowFor(id)).getAllByRole('cell').map(cell => cell.textContent);
    expect(cells('fast')).toEqual(['pgbench', 'Succeeded', '1 min 15 s', 'tps 12 tx/slatency_avg 0.5 ms', 'View']);
    const results = within(rowFor('fast')).getAllByRole('cell')[3];
    expect(within(results).getByText('tps 12 tx/s')).toBeTruthy();
    expect(within(results).getByText('latency_avg 0.5 ms')).toBeTruthy();
    expect(cells('broken')).toEqual(['Sysbench', 'Failed', '20 s', 'benchmark execution failed', 'View']);
    expect(cells('active')).toEqual(['future-tool', 'Running', '—', '—', 'View']);
  });

  it('explains when history is not persistent', async () => {
    const fetch = vi.fn<PluginApi['fetch']>(async () => response({ runs: [] }));
    const { rerender } = await renderHistory(fetch, { persistentHistory: true });
    expect(screen.queryByText(/lost when the service restarts/)).toBeNull();
    expect(screen.getByText('No benchmark runs have been recorded for this instance yet.')).toBeTruthy();
    await rerender({ persistentHistory: false });
    expect(screen.getByText(/lost when the service restarts/)).toBeTruthy();
  });

  it('opens a run once and marks runs that are already shown', async () => {
    const onOpen = vi.fn();
    const fetch = vi.fn<PluginApi['fetch']>(async () => response({ runs: [summary('one'), summary('two')] }));
    await renderHistory(fetch, { onOpen, shownRunIds: ['two'] });
    fireEvent.click(screen.getByRole('button', { name: 'View run one' }));
    expect(onOpen).toHaveBeenCalledWith('one');
    expect((screen.getByRole('button', { name: 'Run two is shown' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('keeps earlier rows while reloading and recovers from a failed load', async () => {
    const reload = deferred<Response>();
    let calls = 0;
    const fetch = vi.fn<PluginApi['fetch']>(async () => {
      calls += 1;
      if (calls === 1) return response({ runs: [summary('kept')] });
      if (calls === 2) return reload.promise;
      return response({ runs: [summary('kept'), summary('newer')] });
    });
    const { rerender } = await renderHistory(fetch);
    await rerender({ refreshKey: 1 });
    expect(screen.getByRole('button', { name: 'View run kept' })).toBeTruthy();
    expect(screen.getByLabelText('Loading benchmark history')).toBeTruthy();
    await act(async () => { reload.resolve(response({}, 503)); });
    expect(screen.getByRole('alert').textContent).toContain('HTTP 503');
    expect(screen.getByRole('button', { name: 'View run kept' })).toBeTruthy();
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Retry' })); });
    expect(screen.queryByRole('alert')).toBeNull();
    expect(screen.getByRole('button', { name: 'View run newer' })).toBeTruthy();
  });

  it('aborts its request on unmount', async () => {
    let signal: AbortSignal | null | undefined;
    const fetch: PluginApi['fetch'] = (_path, init) => { signal = init?.signal; return new Promise<Response>(() => {}); };
    const { view } = await renderHistory(fetch);
    view.unmount();
    expect(signal?.aborted).toBe(true);
  });
});

describe('benchmark tab history refresh', () => {
  it('reloads after starting a run and after it finishes, but not when opening a past run', async () => {
    vi.useFakeTimers();
    let newRunPolls = 0;
    const historyRequests: string[] = [];
    const fetch = vi.fn<PluginApi['fetch']>(async (path, init) => {
      if (path === '/api/status') return response({ drivers: ['pgbench'], storage: 'postgresql' });
      if (path.startsWith('/api/runs?')) {
        historyRequests.push(path);
        return response({ runs: [summary('past')] });
      }
      if (path === '/api/runs' && init?.method === 'POST') return response({ id: 'new', status: 'running' }, 202);
      if (path === '/api/runs/past') return response(summary('past'));
      if (path === '/api/runs/new') {
        newRunPolls += 1;
        return response(newRunPolls === 1 ? summary('new', { status: 'running', completedAt: undefined }) : summary('new'));
      }
      throw new Error(`unexpected request ${path}`);
    });
    await act(async () => {
      render(<BenchmarkTab cluster={{ clusterName: 'main', spec: { providerRef: { name: 'provider-cloudnative-pg' } } }}
        namespace="default" instanceName="pg-1" pluginFetch={fetch} />);
    });
    expect(historyRequests).toEqual(['/api/runs?k8sCluster=main&namespace=default&instance=pg-1&limit=20']);
    expect(screen.queryByText(/lost when the service restarts/)).toBeNull();

    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'View run past' })); });
    await act(async () => { await vi.advanceTimersByTimeAsync(5_000); });
    expect(screen.getByRole('button', { name: 'Run past is shown' })).toBeTruthy();
    expect(historyRequests).toHaveLength(1);

    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Start benchmark' })); });
    expect(historyRequests).toHaveLength(2);
    await act(async () => { await vi.advanceTimersByTimeAsync(5_000); });
    expect(newRunPolls).toBeGreaterThanOrEqual(2);
    expect(historyRequests).toHaveLength(3);
  });
});

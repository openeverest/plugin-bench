import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import type { PluginApi } from '@openeverest/plugin-sdk';
import { BenchmarkTab } from '../src/BenchmarkTab';
import type { BenchmarkRun, BenchmarkRunStatus } from '../src/benchmarkApi';

beforeEach(() => vi.useFakeTimers());
afterEach(() => { cleanup(); vi.useRealTimers(); });

const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const run = (id: string, status: BenchmarkRunStatus, extra: Partial<BenchmarkRun> = {}): BenchmarkRun => ({
  id, status, createdAt: '2026-10-02T00:00:00Z', outputTruncated: false,
  driver: 'pgbench', runnerImage: 'registry/pgbench:v2',
  options: { durationSeconds: 30, clients: 1, threads: 1, scale: 1, initialize: true },
  ...(status === 'running' ? {} : { completedAt: '2026-10-02T00:00:30Z' }),
  ...(status === 'failed' ? { error: 'benchmark execution failed' } : {}),
  ...extra,
});
const tab = (fetch: PluginApi['fetch'], instanceName = 'pg-1') => (
  <BenchmarkTab cluster={{ clusterName: 'main' }} namespace="default" instanceName={instanceName} pluginFetch={fetch} />
);
const start = () => screen.getByRole('button', { name: /Start benchmark|Starting…/ });
const lookup = async (id: string) => {
  fireEvent.change(screen.getByLabelText('Run ID'), { target: { value: id } });
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Find run' })); });
};
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(done => { resolve = done; });
  return { promise, resolve };
}

describe('benchmark driver workflow', () => {
  it('submits the driver envelope and retains independent polling, metrics, and logs for concurrent runs', async () => {
    const requests: Array<{ path: string; method: string; body?: unknown }> = [];
    const gets = new Map<string, number>();
    let accepted = 0;
    const fetch: PluginApi['fetch'] = async (path, init) => {
      requests.push({ path: String(path), method: init?.method ?? 'GET',
        ...(init?.body ? { body: JSON.parse(String(init.body)) } : {}) });
      if (init?.method === 'POST') return response({ id: `run-${++accepted}`, status: 'running' }, 202);
      const id = String(path).split('/').at(-1)!;
      const count = (gets.get(id) ?? 0) + 1;
      gets.set(id, count);
      return response(count === 1 ? run(id, 'running') : run(id, id === 'run-1' ? 'succeeded' : 'failed', {
        metrics: [{ name: 'tps', unit: 'tx/s', value: id === 'run-1' ? 42 : 0 }],
        output: `${id} native output`,
      }));
    };
    render(tab(fetch));
    await act(async () => { fireEvent.click(start()); });
    await act(async () => { fireEvent.click(start()); });
    expect(requests.filter(request => request.method === 'POST').map(request => request.body)).toEqual([
      { target: { k8sCluster: 'main', namespace: 'default', instance: 'pg-1' }, driver: 'pgbench',
        options: { durationSeconds: 30, clients: 1, threads: 1, scale: 1, initialize: true } },
      { target: { k8sCluster: 'main', namespace: 'default', instance: 'pg-1' }, driver: 'pgbench',
        options: { durationSeconds: 30, clients: 1, threads: 1, scale: 1, initialize: true } },
    ]);
    expect([...gets.values()]).toEqual([1, 1]);
    await act(async () => { await vi.advanceTimersByTimeAsync(2_000); });
    const cards = screen.getAllByRole('region', { name: 'Benchmark run result' });
    const first = within(cards.find(card => within(card).queryByText('run-1'))!);
    const second = within(cards.find(card => within(card).queryByText('run-2'))!);
    expect(first.getByRole('status').textContent).toContain('Succeeded');
    expect(second.getByRole('status').textContent).toContain('Failed');
    expect(first.getByRole('cell', { name: '42' })).toBeTruthy();
    expect(second.getByRole('cell', { name: '0' })).toBeTruthy();
    expect(second.getByRole('alert').textContent).toContain('benchmark execution failed');
    fireEvent.click(first.getByRole('button', { name: 'Raw logs' }));
    expect(first.getByLabelText('Benchmark output').textContent).toBe('run-1 native output');
    expect(second.getByRole('button', { name: 'Raw logs' }).getAttribute('aria-expanded')).toBe('false');
    const requestCount = requests.length;
    await lookup('run-1');
    expect(screen.getByText('This run is already shown.')).toBeTruthy();
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(requests).toHaveLength(requestCount);
    expect(screen.getAllByRole('region', { name: 'Benchmark run result' })).toHaveLength(2);
  });

  it('pauses a missing lookup and retries only its GET without starting a new benchmark', async () => {
    const requests: Array<[string, string | undefined]> = [];
    const fetch: PluginApi['fetch'] = async (path, init) => {
      requests.push([String(path), init?.method]);
      return requests.length === 1 ? response({ error: 'benchmark run not found' }, 404)
        : response(run('saved-run', 'succeeded', { output: 'saved output', metrics: undefined }));
    };
    render(tab(fetch));
    await lookup(' saved-run ');
    expect(screen.getByText(/Status updates paused.*benchmark run not found/)).toBeTruthy();
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(requests).toEqual([['/api/runs/saved-run', 'GET']]);
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Retry status check' })); });
    expect(screen.getByRole('status').textContent).toContain('Succeeded');
    expect(screen.getByText('Structured metrics unavailable.')).toBeTruthy();
    expect(requests).toEqual([['/api/runs/saved-run', 'GET'], ['/api/runs/saved-run', 'GET']]);
  });

  it('prevents a second pending submission and aborts the POST on unmount', async () => {
    const accepted = deferred<Response>();
    const requests: Array<{ method?: string; signal?: AbortSignal | null }> = [];
    const fetch: PluginApi['fetch'] = async (_path, init) => {
      requests.push({ method: init?.method, signal: init?.signal });
      return accepted.promise;
    };
    const view = render(tab(fetch));
    await act(async () => { fireEvent.click(start()); });
    expect((start() as HTMLButtonElement).disabled).toBe(true);
    fireEvent.submit(start().closest('form')!);
    expect(requests).toHaveLength(1);
    view.unmount();
    expect(requests[0].signal?.aborted).toBe(true);
    await act(async () => { accepted.resolve(response({ id: 'late-run', status: 'running' }, 202)); });
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(requests.map(request => request.method)).toEqual(['POST']);
  });

  it('aborts status requests on target changes and unmount and ignores late responses', async () => {
    const requests: Array<{ signal?: AbortSignal | null; result: ReturnType<typeof deferred<Response>> }> = [];
    const fetch: PluginApi['fetch'] = async (_path, init) => {
      const result = deferred<Response>();
      requests.push({ signal: init?.signal, result });
      return result.promise;
    };
    const view = render(tab(fetch));
    await lookup('old-run');
    view.rerender(tab(fetch, 'pg-2'));
    expect(requests[0].signal?.aborted).toBe(true);
    await act(async () => { requests[0].result.resolve(response(run('old-run', 'succeeded'))); });
    expect(screen.queryByText('old-run')).toBeNull();
    await lookup('new-run');
    view.unmount();
    expect(requests[1].signal?.aborted).toBe(true);
    await act(async () => { requests[1].result.resolve(response(run('new-run', 'succeeded'))); });
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(requests).toHaveLength(2);
  });

  it('shows a submission rejection without retrying the POST or polling a run', async () => {
    const requests: string[] = [];
    const fetch: PluginApi['fetch'] = async (_path, init) => {
      requests.push(init?.method ?? 'GET');
      return response({ error: 'selected driver does not support this database type' }, 422);
    };
    render(tab(fetch));
    await act(async () => { fireEvent.click(start()); });
    expect(screen.getByRole('alert').textContent).toContain('selected driver does not support this database type');
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(requests).toEqual(['POST']);
    expect(screen.queryByRole('region', { name: 'Benchmark run result' })).toBeNull();
  });
});

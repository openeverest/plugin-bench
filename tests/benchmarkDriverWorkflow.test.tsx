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
// Execution tests assume all registered drivers are enabled and an empty
// history; availability and history are exercised in their own test files.
const fetchWrappers = new WeakMap<PluginApi['fetch'], PluginApi['fetch']>();
const withEnabledDrivers = (fetch: PluginApi['fetch']): PluginApi['fetch'] => {
  if (!fetchWrappers.has(fetch)) fetchWrappers.set(fetch, (path, init) =>
    path === '/api/status' ? Promise.resolve(response({ drivers: ['pgbench', 'sysbench', 'go-ycsb'] }))
      : path.startsWith('/api/runs?') ? Promise.resolve(response({ runs: [] })) : fetch(path, init));
  return fetchWrappers.get(fetch)!;
};
const renderReady = async (element: React.ReactElement) => {
  let view!: ReturnType<typeof render>;
  await act(async () => { view = render(element); });
  return view;
};
const tab = (fetch: PluginApi['fetch'], instanceName = 'pg-1', provider = 'provider-cloudnative-pg') => (
  <BenchmarkTab cluster={{ clusterName: 'main', spec: { providerRef: { name: provider } } }}
    namespace="default" instanceName={instanceName} pluginFetch={withEnabledDrivers(fetch)} />
);
const workflowTargets = [
  { driver: 'pgbench', provider: 'provider-cloudnative-pg', instanceName: 'pg-1',
    options: { durationSeconds: 30, clients: 1, threads: 1, scale: 1, initialize: true } },
  { driver: 'sysbench', provider: 'percona-server-mysql', instanceName: 'mysql-1',
    options: { durationSeconds: 30, threads: 1, tables: 1, tableSize: 10000, initialize: true } },
  { driver: 'go-ycsb', provider: 'percona-server-mongodb', instanceName: 'mongo-1',
    options: { collection: 'usertable', recordCount: 10000, operationCount: 20000, threads: 1, initialize: true } },
];
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
  it.each([
    { provider: 'provider-cloudnative-pg', driver: 'pgbench' },
    { provider: 'provider-percona-postgresql', driver: 'pgbench' },
    { provider: 'percona-server-mysql', driver: 'Sysbench' },
    { provider: 'percona-xtradb-cluster', driver: 'Sysbench' },
    { provider: 'percona-server-mongodb', driver: 'go-ycsb' },
  ])('offers only $driver for $provider', async ({ provider, driver }) => {
    const fetch = vi.fn<PluginApi['fetch']>();
    await renderReady(tab(fetch, 'instance-1', provider));
    const picker = screen.getByRole('combobox', { name: 'Benchmark driver' });
    expect(picker.textContent).toBe(driver);
    fireEvent.mouseDown(picker);
    const options = screen.getAllByRole('option');
    expect(options).toHaveLength(1);
    expect(options[0].textContent).toBe(driver);
    expect(fetch).not.toHaveBeenCalled();
  });

  it.each([
    { cluster: undefined, message: 'Waiting for database provider information.' },
    { cluster: { spec: { providerRef: { name: 'unsupported-provider' } } }, message: 'Benchmarking is not available for this database provider.' },
  ])('guards an unavailable provider while preserving run lookup: $message', ({ cluster, message }) => {
    const fetch = vi.fn<PluginApi['fetch']>();
    render(<BenchmarkTab cluster={cluster} namespace="default" instanceName="instance-1" pluginFetch={fetch} />);
    expect(screen.getByText(message)).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Start benchmark' })).toBeNull();
    expect(screen.queryByRole('combobox', { name: 'Benchmark driver' })).toBeNull();
    expect(screen.getByRole('button', { name: 'Find run' })).toBeTruthy();
    expect(fetch).not.toHaveBeenCalled();
  });

  it.each([
    { provider: 'percona-server-mysql', label: 'Sysbench', databaseRequired: true },
    { provider: 'percona-server-mongodb', label: 'go-ycsb', databaseRequired: false },
  ])('initializes $label when provider metadata arrives', async ({ provider, label, databaseRequired }) => {
    const fetch = vi.fn<PluginApi['fetch']>();
    const view = render(<BenchmarkTab cluster={{ clusterName: 'main' }} namespace="default" instanceName="mysql-1" pluginFetch={fetch} />);
    expect(screen.getByText('Waiting for database provider information.')).toBeTruthy();
    await act(async () => { view.rerender(tab(fetch, 'mysql-1', provider)); });
    expect(screen.queryByText('Waiting for database provider information.')).toBeNull();
    expect(screen.getByRole('combobox', { name: 'Benchmark driver' }).textContent).toBe(label);
    expect((start() as HTMLButtonElement).disabled).toBe(databaseRequired);
    if (databaseRequired) fireEvent.change(screen.getByLabelText('Database'), { target: { value: 'sbtest' } });
    else expect((screen.getByLabelText('Database') as HTMLInputElement).value).toBe('ycsb');
    expect((start() as HTMLButtonElement).disabled).toBe(false);
    expect(fetch).not.toHaveBeenCalled();
  });

  it.each([
    { provider: 'percona-server-mysql', label: 'Sysbench', field: 'Duration (seconds)', value: '30' },
    { provider: 'percona-server-mongodb', label: 'go-ycsb', field: 'Records', value: '10000' },
  ])('preserves refresh edits but aborts and resets on a change to $provider', async ({ provider, label, field, value }) => {
    const accepted = deferred<Response>();
    const requests: Array<{ method?: string; signal?: AbortSignal | null }> = [];
    const fetch: PluginApi['fetch'] = async (_path, init) => {
      requests.push({ method: init?.method, signal: init?.signal });
      return accepted.promise;
    };
    const view = await renderReady(tab(fetch));
    fireEvent.change(screen.getByLabelText('Duration (seconds)'), { target: { value: '5' } });
    await act(async () => { fireEvent.click(start()); });
    await act(async () => { view.rerender(tab(fetch)); });
    expect(requests[0].signal?.aborted).toBe(false);
    expect((screen.getByLabelText('Duration (seconds)') as HTMLInputElement).value).toBe('5');
    await act(async () => { view.rerender(tab(fetch, 'pg-1', provider)); });
    expect(requests[0].signal?.aborted).toBe(true);
    expect(screen.getByRole('combobox', { name: 'Benchmark driver' }).textContent).toBe(label);
    expect((screen.getByLabelText(field) as HTMLInputElement).value).toBe(value);
    await act(async () => { accepted.resolve(response({ id: 'old-run', status: 'running' }, 202)); });
    expect(screen.queryByRole('region', { name: 'Benchmark run result' })).toBeNull();
    expect(requests.map(request => request.method)).toEqual(['POST']);
  });

  it.each(workflowTargets)('$driver aborts an old status lookup when the provider changes', async ({ provider, instanceName }) => {
    const pending = deferred<Response>();
    let signal: AbortSignal | null | undefined;
    const fetch = vi.fn<PluginApi['fetch']>(async (_path, init) => {
      signal = init?.signal;
      return pending.promise;
    });
    const view = await renderReady(tab(fetch, instanceName, provider));
    await lookup('old-run');
    const nextProvider = provider === 'percona-server-mongodb' ? 'percona-server-mysql' : 'percona-server-mongodb';
    await act(async () => { view.rerender(tab(fetch, instanceName, nextProvider)); });
    expect(signal?.aborted).toBe(true);
    await act(async () => { pending.resolve(response(run('old-run', 'succeeded'))); });
    expect(screen.queryByRole('region', { name: 'Benchmark run result' })).toBeNull();
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it.each(workflowTargets)('$driver retains independent polling, metrics, and logs for concurrent runs', async ({ driver, provider, instanceName, options }) => {
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
      const metadata = { driver, runnerImage: `registry/${driver}:v2`, options };
      return response(count === 1 ? run(id, 'running', metadata) : run(id, id === 'run-1' ? 'succeeded' : 'failed', {
        ...metadata,
        metrics: [{ name: driver === 'go-ycsb' ? 'ops_per_second' : 'tps', unit: driver === 'go-ycsb' ? 'ops/s' : 'tx/s', value: id === 'run-1' ? 42 : 0 }],
        output: `${id} native output`,
      }));
    };
    await renderReady(tab(fetch, instanceName, provider));
    if (driver === 'sysbench') fireEvent.change(screen.getByLabelText('Database'), { target: { value: 'sbtest' } });
    await act(async () => { fireEvent.click(start()); });
    await act(async () => { fireEvent.click(start()); });
    const expected = { target: { k8sCluster: 'main', namespace: 'default', instance: instanceName },
      ...(driver === 'sysbench' ? { database: 'sbtest' } : driver === 'go-ycsb' ? { database: 'ycsb' } : {}), driver, options };
    expect(requests.filter(request => request.method === 'POST').map(request => request.body)).toEqual([expected, expected]);
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

  it.each(workflowTargets)('$driver pauses a missing lookup and retries only its GET', async ({ driver, provider, instanceName, options }) => {
    const requests: Array<[string, string | undefined]> = [];
    const fetch: PluginApi['fetch'] = async (path, init) => {
      requests.push([String(path), init?.method]);
      return requests.length === 1 ? response({ error: 'benchmark run not found' }, 404)
        : response(run('saved-run', 'succeeded', { driver, options, output: 'saved output', metrics: undefined }));
    };
    await renderReady(tab(fetch, instanceName, provider));
    await lookup(' saved-run ');
    expect(screen.getByText(/Status updates paused.*benchmark run not found/)).toBeTruthy();
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(requests).toEqual([['/api/runs/saved-run', 'GET']]);
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Retry status check' })); });
    expect(screen.getByRole('status').textContent).toContain('Succeeded');
    expect(screen.getByText('Structured metrics unavailable.')).toBeTruthy();
    expect(requests).toEqual([['/api/runs/saved-run', 'GET'], ['/api/runs/saved-run', 'GET']]);
  });

  it.each(workflowTargets)('$driver prevents duplicate submission and aborts the POST on unmount', async ({ provider, instanceName }) => {
    const accepted = deferred<Response>();
    const requests: Array<{ method?: string; signal?: AbortSignal | null }> = [];
    const fetch: PluginApi['fetch'] = async (_path, init) => {
      requests.push({ method: init?.method, signal: init?.signal });
      return accepted.promise;
    };
    const view = await renderReady(tab(fetch, instanceName, provider));
    if (provider === 'percona-server-mysql') fireEvent.change(screen.getByLabelText('Database'), { target: { value: 'sbtest' } });
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

  it.each(workflowTargets)('$driver aborts status requests on target changes and unmount', async ({ provider, instanceName }) => {
    const requests: Array<{ signal?: AbortSignal | null; result: ReturnType<typeof deferred<Response>> }> = [];
    const fetch: PluginApi['fetch'] = async (_path, init) => {
      const result = deferred<Response>();
      requests.push({ signal: init?.signal, result });
      return result.promise;
    };
    const view = await renderReady(tab(fetch, instanceName, provider));
    await lookup('old-run');
    await act(async () => { view.rerender(tab(fetch, `${instanceName}-next`, provider)); });
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
    await renderReady(tab(fetch));
    await act(async () => { fireEvent.click(start()); });
    expect(screen.getByRole('alert').textContent).toContain('selected driver does not support this database type');
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(requests).toEqual(['POST']);
    expect(screen.queryByRole('region', { name: 'Benchmark run result' })).toBeNull();
  });
});

describe('MongoDB go-ycsb workflow', () => {
  const mongoTab = (fetch: PluginApi['fetch']) => tab(fetch, 'mongo-1', 'percona-server-mongodb');
  const change = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });
  const options = { collection: 'benchmark_records', recordCount: 1000, operationCount: 2000, threads: 2, initialize: true };
  const snapshot = (id: string, status: BenchmarkRunStatus, extra: Partial<BenchmarkRun> = {}) => run(id, status, {
    driver: 'go-ycsb', database: 'ycsb', runnerImage: 'registry/go-ycsb:v2', options, ...extra,
  });

  it.each([true, false])('submits loading=%s and shows recorded options, logs, and operation metrics', async initialize => {
    const requests: Array<{ path: string; init?: RequestInit }> = [];
    let gets = 0;
    const fetch: PluginApi['fetch'] = async (path, init) => {
      requests.push({ path, init });
      if (init?.method === 'POST') return response({ id: 'mongo-run', status: 'running' }, 202);
      return response(snapshot('mongo-run', ++gets === 1 ? 'running' : 'succeeded', {
        options: { ...options, initialize },
        metrics: [{ name: 'ops_per_second', unit: 'ops/s', value: 1234.5 }, { name: 'latency_avg', unit: 'ms', value: 1.25 }],
        output: 'TOTAL Count: 2000, OPS: 1234.5',
      }));
    };
    await renderReady(mongoTab(fetch));
    change('Collection', options.collection);
    change('Records', '1000');
    change('Operations', '2000');
    change('Threads', '2');
    if (!initialize) fireEvent.click(screen.getByRole('checkbox', { name: 'Load benchmark data' }));
    fireEvent.click(screen.getByRole('button', { name: 'Job resources' }));
    change('CPU request (millicores)', '500');
    change('Memory limit (MiB)', '1024');
    fireEvent.click(screen.getByRole('button', { name: /Job placement/ }));
    fireEvent.click(screen.getByRole('switch', { name: 'Run on nodes with specific labels' }));
    change('Label key 1', 'pool');
    change('Label value 1', 'benchmark');
    await act(async () => { fireEvent.click(start()); });
    expect(requests.map(request => [request.path, request.init?.method])).toEqual([
      ['/api/runs', 'POST'], ['/api/runs/mongo-run', 'GET'],
    ]);
    expect(JSON.parse(String(requests[0].init?.body))).toEqual({
      target: { k8sCluster: 'main', namespace: 'default', instance: 'mongo-1' },
      database: 'ycsb', driver: 'go-ycsb', options: { ...options, initialize },
      resources: { cpuRequest: '500m', memoryLimit: '1024Mi' },
      nodeAffinity: { requiredDuringSchedulingIgnoredDuringExecution: { nodeSelectorTerms: [{
        matchExpressions: [{ key: 'pool', operator: 'In', values: ['benchmark'] }],
      }] } },
    });
    expect(requests[0].init?.body).not.toMatch(/durationSeconds|clients|scale|tables|tableSize|MONGODB_URI|password/);
    change('Collection', 'different_draft');
    change('Records', '900');
    await act(async () => { await vi.advanceTimersByTimeAsync(2_000); });
    const card = within(screen.getByRole('region', { name: 'Benchmark run result' }));
    expect(card.getByRole('status').textContent).toContain('Succeeded');
    expect(card.getByText('registry/go-ycsb:v2')).toBeTruthy();
    expect(card.getByText('ycsb')).toBeTruthy();
    const effective = within(card.getByRole('region', { name: 'Effective benchmark options' }));
    expect(effective.getByText('benchmark_records')).toBeTruthy();
    expect(effective.getByText('1000')).toBeTruthy();
    expect(effective.getByText('2000')).toBeTruthy();
    expect(effective.getByText(String(initialize))).toBeTruthy();
    expect(effective.queryByText('different_draft')).toBeNull();
    expect(effective.queryByText('900')).toBeNull();
    const metrics = within(card.getByRole('table', { name: 'Benchmark metrics' }));
    expect(metrics.getByRole('rowheader', { name: 'ops_per_second' })).toBeTruthy();
    expect(metrics.getByRole('rowheader', { name: 'latency_avg' })).toBeTruthy();
    expect(metrics.getByRole('cell', { name: 'ops/s' })).toBeTruthy();
    expect(metrics.getByRole('cell', { name: 'ms' })).toBeTruthy();
    expect(metrics.getByRole('cell', { name: (1234.5).toLocaleString(undefined, { maximumFractionDigits: 2 }) })).toBeTruthy();
    expect(metrics.queryByRole('rowheader', { name: 'tps' })).toBeNull();
    expect(card.getByRole('button', { name: 'Raw logs' }).getAttribute('aria-expanded')).toBe('false');
    fireEvent.click(card.getByRole('button', { name: 'Raw logs' }));
    expect(card.getByLabelText('Benchmark output').textContent).toBe('TOTAL Count: 2000, OPS: 1234.5');
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(requests).toHaveLength(3);
  });

  it.each([
    { status: 400, message: 'driver is not available' },
    { status: 422, message: 'driver does not support the target database type' },
    { status: 502, message: 'MongoDB connection URI is invalid' },
    { status: 503, message: 'benchmark capacity is currently full' },
    { status: 0, message: 'The connection to the benchmark service was lost.' },
    { status: 202, message: 'The benchmark service returned an invalid response.' },
  ])('retains the draft without polling or retrying POST after $status: $message', async ({ status, message }) => {
    const fetch = vi.fn<PluginApi['fetch']>(async () => {
      if (status === 0) throw new TypeError('Network disconnected');
      return response({ error: message }, status);
    });
    await renderReady(mongoTab(fetch));
    change('Collection', 'fresh_records');
    change('Records', '1000');
    change('Operations', '2000');
    change('Threads', '2');
    await act(async () => { fireEvent.click(start()); });
    expect(screen.getByRole('alert').textContent).toContain(message);
    for (const [label, value] of [['Collection', 'fresh_records'], ['Records', '1000'], ['Operations', '2000'], ['Threads', '2'], ['Database', 'ycsb']]) {
      expect((screen.getByLabelText(label) as HTMLInputElement).value).toBe(value);
    }
    expect(screen.queryByRole('region', { name: 'Benchmark run result' })).toBeNull();
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch.mock.calls[0][1]?.method).toBe('POST');
  });

  it.each([
    { status: 'failed' as const, initialize: true, output: 'load failed: duplicate key', truncated: true },
    { status: 'failed' as const, initialize: false, output: 'run failed: not authorized on ycsb', truncated: false },
    { status: 'succeeded' as const, initialize: false, output: 'benchmark complete', truncated: false },
  ])('preserves $status and logs without inventing metrics: $output', async ({ status, initialize, output, truncated }) => {
    const fetch = vi.fn<PluginApi['fetch']>(async (_path, init) => init?.method === 'POST'
      ? response({ id: 'mongo-run', status: 'running' }, 202)
      : response(snapshot('mongo-run', status, { options: { ...options, initialize }, output, outputTruncated: truncated })));
    await renderReady(mongoTab(fetch));
    if (!initialize) fireEvent.click(screen.getByRole('checkbox', { name: 'Load benchmark data' }));
    await act(async () => { fireEvent.click(start()); });
    const card = within(screen.getByRole('region', { name: 'Benchmark run result' }));
    expect(card.getByRole('status').textContent).toContain(status === 'failed' ? 'Failed' : 'Succeeded');
    expect(card.getByText('Structured metrics unavailable.')).toBeTruthy();
    expect(card.queryByRole('table')).toBeNull();
    if (status === 'failed') expect(card.getByRole('alert').textContent).toContain('benchmark execution failed');
    else expect(card.queryByRole('alert')).toBeNull();
    fireEvent.click(card.getByRole('button', { name: /Raw logs/ }));
    expect(card.getByLabelText('Benchmark output').textContent).toBe(output);
    if (truncated) expect(card.getByText('Output was truncated by the backend.')).toBeTruthy();
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(fetch).toHaveBeenCalledTimes(2);
  });
});

describe('MySQL Sysbench workflow', () => {
  const options = { durationSeconds: 5, threads: 2, tables: 3, tableSize: 500, initialize: false };
  const snapshot = (id: string, status: BenchmarkRunStatus, extra: Partial<BenchmarkRun> = {}) => run(id, status, {
    driver: 'sysbench', runnerImage: 'registry/sysbench:v2', database: 'sbtest', options, ...extra,
  });
  const mysqlTab = (fetch: PluginApi['fetch']) => tab(fetch, 'mysql-1', 'percona-server-mysql');
  const change = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });

  it('submits the edited driver envelope with shared settings and displays returned effective results', async () => {
    const requests: Array<{ path: string; init?: RequestInit }> = [];
    let gets = 0;
    const fetch: PluginApi['fetch'] = async (path, init) => {
      requests.push({ path, init });
      if (init?.method === 'POST') return response({ id: 'mysql-run', status: 'running' }, 202);
      return response(snapshot('mysql-run', ++gets === 1 ? 'running' : 'succeeded', {
        metrics: [{ name: 'tps', unit: 'tx/s', value: 1234.5 }, { name: 'latency_avg', unit: 'ms', value: 3.2 }],
        output: 'oltp_read_write completed',
      }));
    };
    await renderReady(mysqlTab(fetch));
    fireEvent.click(screen.getByRole('checkbox', { name: 'Prepare Sysbench tables' }));
    change('Database', ' sbtest ');
    change('Duration (seconds)', '5');
    change('Threads', '2');
    change('Tables', '3');
    change('Rows per table', '500');
    fireEvent.click(screen.getByRole('button', { name: 'Job resources' }));
    change('CPU request (millicores)', '500');
    change('Memory limit (MiB)', '1024');
    fireEvent.click(screen.getByRole('button', { name: /Job placement/ }));
    fireEvent.click(screen.getByRole('switch', { name: 'Run on nodes with specific labels' }));
    change('Label key 1', 'pool');
    change('Label value 1', 'benchmark');
    await act(async () => { fireEvent.click(start()); });
    expect(requests.map(request => [request.path, request.init?.method])).toEqual([
      ['/api/runs', 'POST'], ['/api/runs/mysql-run', 'GET'],
    ]);
    expect(JSON.parse(String(requests[0].init?.body))).toEqual({
      target: { k8sCluster: 'main', namespace: 'default', instance: 'mysql-1' }, database: 'sbtest',
      driver: 'sysbench', options, resources: { cpuRequest: '500m', memoryLimit: '1024Mi' },
      nodeAffinity: { requiredDuringSchedulingIgnoredDuringExecution: { nodeSelectorTerms: [{
        matchExpressions: [{ key: 'pool', operator: 'In', values: ['benchmark'] }],
      }] } },
    });
    expect(requests[0].init?.body).not.toMatch(/clients|scale|MYSQL_|password/);
    // The form can change while a run is active; its results must use the saved snapshot.
    change('Rows per table', '900');
    await act(async () => { await vi.advanceTimersByTimeAsync(2_000); });
    const card = within(screen.getByRole('region', { name: 'Benchmark run result' }));
    expect(card.getByRole('status').textContent).toContain('Succeeded');
    expect(card.getByText('registry/sysbench:v2')).toBeTruthy();
    const effective = within(card.getByRole('region', { name: 'Effective benchmark options' }));
    expect(effective.getByText('500')).toBeTruthy();
    expect(effective.getByText('3')).toBeTruthy();
    expect(effective.getByText('false')).toBeTruthy();
    expect(effective.queryByText('900')).toBeNull();
    expect(effective.queryByText('From existing tables')).toBeNull();
    const metrics = within(card.getByRole('table', { name: 'Benchmark metrics' }));
    expect(metrics.getByRole('rowheader', { name: 'tps' })).toBeTruthy();
    expect(metrics.getByRole('rowheader', { name: 'latency_avg' })).toBeTruthy();
    expect(metrics.getByRole('cell', { name: (1234.5).toLocaleString(undefined, { maximumFractionDigits: 2 }) })).toBeTruthy();
    expect(metrics.getByRole('cell', { name: (3.2).toLocaleString(undefined, { maximumFractionDigits: 2 }) })).toBeTruthy();
    expect(metrics.getByRole('cell', { name: 'tx/s' })).toBeTruthy();
    expect(metrics.getByRole('cell', { name: 'ms' })).toBeTruthy();
    const logs = card.getByRole('button', { name: 'Raw logs' });
    expect(logs.getAttribute('aria-expanded')).toBe('false');
    fireEvent.click(logs);
    expect(card.getByLabelText('Benchmark output').textContent).toBe('oltp_read_write completed');
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(requests).toHaveLength(3);
  });

  it.each([
    { status: 400, message: 'selected driver is disabled' },
    { status: 422, message: 'sysbench requires a MySQL target' },
    { status: 400, message: 'database cannot be represented in the sysbench configuration file' },
    { status: 503, message: 'benchmark capacity is currently full' },
    { status: 0, message: 'The connection to the benchmark service was lost.' },
  ])('retains inputs and avoids retries or polling after rejection: $message', async ({ status, message }) => {
    const fetch = vi.fn<PluginApi['fetch']>(async () => {
      if (status === 0) throw new TypeError('Network disconnected');
      return response({ error: message }, status);
    });
    await renderReady(mysqlTab(fetch));
    change('Database', 'sbtest');
    change('Threads', '4');
    change('Tables', '3');
    change('Rows per table', '500');
    await act(async () => { fireEvent.click(start()); });
    expect(screen.getByRole('alert').textContent).toContain(message);
    expect((screen.getByLabelText('Threads') as HTMLInputElement).value).toBe('4');
    expect((screen.getByLabelText('Tables') as HTMLInputElement).value).toBe('3');
    expect((screen.getByLabelText('Rows per table') as HTMLInputElement).value).toBe('500');
    expect((screen.getByLabelText('Database') as HTMLInputElement).value).toBe('sbtest');
    expect(screen.queryByRole('region', { name: 'Benchmark run result' })).toBeNull();
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch.mock.calls[0][1]?.method).toBe('POST');
  });

  it.each(['succeeded', 'failed'] as const)('retains %s status and logs when metrics are unavailable', async status => {
    const output = status === 'failed' ? 'prepare failed: table sbtest1 already exists' : 'benchmark completed';
    const fetch = vi.fn<PluginApi['fetch']>(async (_path, init) => init?.method === 'POST'
      ? response({ id: 'mysql-run', status: 'running' }, 202)
      : response(snapshot('mysql-run', status, {
        options: { durationSeconds: 30, threads: 1, tables: 1, tableSize: 10000, initialize: status === 'failed' },
        output, outputTruncated: status === 'failed',
      })));
    await renderReady(mysqlTab(fetch));
    change('Database', 'sbtest');
    if (status === 'succeeded') fireEvent.click(screen.getByRole('checkbox', { name: 'Prepare Sysbench tables' }));
    await act(async () => { fireEvent.click(start()); });
    const card = within(screen.getByRole('region', { name: 'Benchmark run result' }));
    expect(card.getByRole('status').textContent).toContain(status === 'failed' ? 'Failed' : 'Succeeded');
    expect(card.getByText('Structured metrics unavailable.')).toBeTruthy();
    expect(card.queryByRole('table')).toBeNull();
    if (status === 'failed') expect(card.getByRole('alert').textContent).toContain('benchmark execution failed');
    else expect(card.queryByRole('alert')).toBeNull();
    fireEvent.click(card.getByRole('button', { name: /Raw logs/ }));
    expect(card.getByLabelText('Benchmark output').textContent).toBe(output);
    if (status === 'failed') expect(card.getByText('Output was truncated by the backend.')).toBeTruthy();
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(fetch).toHaveBeenCalledTimes(2);
  });

  it.each([
    { formProvider: 'percona-server-mysql', driver: 'pgbench', options: { scale: 1, initialize: false }, displayed: 'From existing tables' },
    { formProvider: 'provider-cloudnative-pg', driver: 'sysbench', options, displayed: '500' },
    { formProvider: 'percona-server-mysql', driver: 'future-driver', options: { workers: 7 }, displayed: '7' },
    { formProvider: 'provider-cloudnative-pg', driver: 'go-ycsb', options: { collection: 'saved_mongo', recordCount: 123, initialize: false }, displayed: 'saved_mongo' },
    { formProvider: 'percona-server-mongodb', driver: 'pgbench', options: { scale: 1, initialize: false }, displayed: 'From existing tables' },
  ])('uses the saved $driver snapshot when looking up a run under $formProvider', async ({ formProvider, driver, options: recorded, displayed }) => {
    const fetch = vi.fn<PluginApi['fetch']>(async () => response(run('saved-run', 'succeeded', {
      driver, runnerImage: `registry/${driver}:v2`, options: recorded, output: 'saved logs',
    })));
    await renderReady(tab(fetch, 'instance-1', formProvider));
    await lookup('saved-run');
    const card = within(screen.getByRole('region', { name: 'Benchmark run result' }));
    expect(card.getByText(driver)).toBeTruthy();
    expect(within(card.getByRole('region', { name: 'Effective benchmark options' })).getByText(displayed)).toBeTruthy();
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch.mock.calls[0][1]?.method).toBe('GET');
  });
});

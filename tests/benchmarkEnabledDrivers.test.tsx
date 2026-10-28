import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import type { PluginApi } from '@openeverest/plugin-sdk';
import { BenchmarkTab } from '../src/BenchmarkTab';
import { getEnabledBenchmarkDrivers } from '../src/benchmarkDriversApi';

afterEach(() => { cleanup(); vi.useRealTimers(); });
const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const tab = (fetch: PluginApi['fetch'], provider = 'percona-server-mysql') => (
  <BenchmarkTab cluster={{ clusterName: 'main', spec: { providerRef: { name: provider } } }}
    namespace="default" instanceName="instance-1" pluginFetch={fetch} />
);

describe('backend-enabled benchmark drivers', () => {
  it.each([
    { provider: 'percona-server-mysql', ids: ['sysbench', 'future'], label: 'Sysbench' },
    { provider: 'percona-xtradb-cluster', ids: ['pgbench', 'sysbench'], label: 'Sysbench' },
    { provider: 'provider-cloudnative-pg', ids: ['pgbench', 'sysbench'], label: 'pgbench' },
  ])('intersects enabled drivers with $provider support', async ({ provider, ids, label }) => {
    const fetch = vi.fn<PluginApi['fetch']>(async () => response({ drivers: ids }));
    await act(async () => { render(tab(fetch, provider)); });
    expect(screen.getByRole('combobox', { name: 'Benchmark driver' }).textContent).toBe(label);
    expect(fetch).toHaveBeenCalledWith('/api/status', expect.objectContaining({ method: 'GET' }));
  });

  it.each(['percona-server-mysql', 'percona-xtradb-cluster'].flatMap(provider =>
    [[], ['pgbench'], ['future']].map(ids => ({ provider, ids }))))(
    'hides a disabled driver for $provider and enabled IDs $ids', async ({ provider, ids }) => {
      const fetch = vi.fn<PluginApi['fetch']>(async () => response({ drivers: ids }));
      await act(async () => { render(tab(fetch, provider)); });
      expect(screen.getByText('No benchmark driver is enabled for this database provider.')).toBeTruthy();
      expect(screen.queryByRole('button', { name: 'Start benchmark' })).toBeNull();
      expect(screen.getByRole('button', { name: 'Find run' })).toBeTruthy();
    },
  );

  it('waits for the backend and aborts the request on unmount', async () => {
    let resolve!: (value: Response) => void;
    let signal: AbortSignal | null | undefined;
    const fetch: PluginApi['fetch'] = async (_path, init) => {
      signal = init?.signal;
      return new Promise<Response>(done => { resolve = done; });
    };
    const view = render(tab(fetch));
    expect(screen.getByText('Checking available benchmark drivers…')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Start benchmark' })).toBeNull();
    view.unmount();
    expect(signal?.aborted).toBe(true);
    await act(async () => { resolve(response({ drivers: ['sysbench'] })); });
  });

  it('retries a failed check without submitting a run', async () => {
    const fetch = vi.fn<PluginApi['fetch']>()
      .mockResolvedValueOnce(response({ error: 'unavailable' }, 503))
      .mockResolvedValueOnce(response({ drivers: ['sysbench'] }));
    await act(async () => { render(tab(fetch)); });
    expect(screen.getByRole('alert').textContent).toContain('HTTP 503');
    expect(screen.getByRole('button', { name: 'Find run' })).toBeTruthy();
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Retry driver check' })); });
    expect(screen.getByRole('combobox', { name: 'Benchmark driver' }).textContent).toBe('Sysbench');
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(fetch.mock.calls.every(([path, init]) => path === '/api/status' && init?.method === 'GET')).toBe(true);
  });

  it('ignores an old driver response after the provider changes', async () => {
    let resolve!: (value: Response) => void;
    let oldSignal: AbortSignal | null | undefined;
    const fetch = vi.fn<PluginApi['fetch']>()
      .mockImplementationOnce(async (_path, init) => {
        oldSignal = init?.signal;
        return new Promise<Response>(done => { resolve = done; });
      })
      .mockResolvedValueOnce(response({ drivers: ['pgbench'] }));
    const view = render(tab(fetch));
    await act(async () => { view.rerender(tab(fetch, 'provider-cloudnative-pg')); });
    expect(oldSignal?.aborted).toBe(true);
    await act(async () => { resolve(response({ drivers: ['sysbench'] })); });
    expect(screen.getByRole('combobox', { name: 'Benchmark driver' }).textContent).toBe('pgbench');
  });

  it.each([{}, { drivers: null }, { drivers: 'sysbench' }, { drivers: [1] }, { drivers: [''] }])(
    'fails closed for malformed status: %j', async body => {
      await act(async () => { render(tab(async () => response(body))); });
      expect(screen.getByRole('alert').textContent).toContain('invalid driver list');
      expect(screen.queryByRole('button', { name: 'Start benchmark' })).toBeNull();
    },
  );

  it('bounds the driver check even when fetch ignores abort', async () => {
    vi.useFakeTimers();
    const fetch: PluginApi['fetch'] = () => new Promise(() => {});
    const result = expect(getEnabledBenchmarkDrivers(fetch, { timeoutMs: 10 })).rejects.toThrow('timed out');
    await vi.advanceTimersByTimeAsync(10);
    await result;
  });
});

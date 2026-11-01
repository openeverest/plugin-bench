import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { BenchmarkPage } from '../src/BenchmarkPage';

const basePath = '/v1/clusters/main/plugins/plugin-bench';
const pg = { k8sCluster: 'main', namespace: 'default', name: 'postgres', provider: 'provider-percona-postgresql' };
const mysql = { ...pg, name: 'mysql', provider: 'percona-server-mysql' };
const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
function fetchFor(instances: unknown[] = [pg], drivers = ['pgbench', 'sysbench']) {
  return vi.fn(async (path: string) => response(path === '/api/status' ? { drivers } : { instances }));
}
afterEach(cleanup);

describe('benchmark sidebar page', () => {
  it('waits for both discovery and enabled drivers', async () => {
    let finish!: (value: Response) => void;
    const fetch = vi.fn(async (path: string) => path === '/api/status'
      ? new Promise<Response>(resolve => { finish = resolve; }) : response({ instances: [pg] }));
    render(<BenchmarkPage pluginFetch={fetch} basePath={basePath} />);
    expect(screen.getByRole('status').textContent).toContain('Loading');
    expect(screen.queryByRole('table')).toBeNull();
    await act(async () => { finish(response({ drivers: ['pgbench'] })); });
    expect(await screen.findByRole('table')).toBeTruthy();
  });

  it('shows supported providers, deduplicates targets, and links to their benchmark tabs', async () => {
    const fetch = fetchFor([pg, mysql, { ...mysql, name: 'pxc', provider: 'percona-xtradb-cluster' },
      { ...pg, namespace: 'other' }, { ...pg }, { ...pg, name: 'mongo', provider: 'unknown' }]);
    render(<BenchmarkPage pluginFetch={fetch} basePath={basePath} />);
    const table = await screen.findByRole('table');
    expect(within(table).getAllByRole('row')).toHaveLength(5);
    expect(screen.queryByText('mongo')).toBeNull();
    expect(screen.getByRole('link', { name: 'Benchmark other/postgres' }).getAttribute('href'))
      .toBe('/databases/other/postgres/performance-benchmark');
    expect(screen.getByRole('link', { name: 'mysql' }).getAttribute('href'))
      .toBe('/databases/default/mysql/performance-benchmark');
    expect(fetch.mock.calls.map(([path]) => path)).toEqual(['/api/instances?k8sCluster=main', '/api/status']);
  });

  it('hides instances whose driver is disabled', async () => {
    render(<BenchmarkPage pluginFetch={fetchFor([pg, mysql], ['pgbench'])} basePath={basePath} />);
    await screen.findByRole('table');
    expect(screen.queryByRole('link', { name: 'mysql' })).toBeNull();
  });

  it.each([{ instances: [] }, { instances: [mysql] }])('shows an empty state when no supported enabled targets remain', async ({ instances }) => {
    render(<BenchmarkPage pluginFetch={fetchFor(instances, ['pgbench'])} basePath={basePath} />);
    expect(await screen.findByText(/No accessible database instances/)).toBeTruthy();
    expect(screen.queryByRole('table')).toBeNull();
  });

  it.each(['/api/status', '/api/instances?k8sCluster=main'])('retries both requests after %s fails', async failingPath => {
    let fail = true;
    const fetch = vi.fn(async (path: string) => fail && path === failingPath
      ? response({}, 503) : response(path === '/api/status' ? { drivers: ['pgbench'] } : { instances: [pg] }));
    render(<BenchmarkPage pluginFetch={fetch} basePath={basePath} />);
    expect((await screen.findByRole('alert')).textContent).toContain('HTTP 503');
    expect(screen.queryByText(/No accessible/)).toBeNull();
    fail = false;
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    await screen.findByRole('table');
    expect(fetch.mock.calls.filter(([path]) => path === '/api/status')).toHaveLength(2);
    expect(fetch.mock.calls.filter(([path]) => path.startsWith('/api/instances'))).toHaveLength(2);
  });

  it('aborts on unmount', () => {
    const signals: AbortSignal[] = [];
    const fetch = vi.fn((_path: string, init?: RequestInit) => {
      signals.push(init!.signal as AbortSignal);
      return new Promise<Response>(() => {});
    });
    const { unmount } = render(<BenchmarkPage pluginFetch={fetch} basePath={basePath} />);
    unmount();
    expect(signals).toHaveLength(2);
    expect(signals.every(signal => signal.aborted)).toBe(true);
  });

  it('ignores a late response after the cluster changes', async () => {
    let finish!: (value: Response) => void;
    const fetch = vi.fn(async (path: string) => {
      if (path === '/api/status') return response({ drivers: ['pgbench'] });
      if (path.endsWith('=main')) return new Promise<Response>(resolve => { finish = resolve; });
      return response({ instances: [{ ...pg, name: 'new-db', k8sCluster: 'next' }] });
    });
    const { rerender } = render(<BenchmarkPage pluginFetch={fetch} basePath={basePath} />);
    rerender(<BenchmarkPage pluginFetch={fetch} basePath="/v1/clusters/next/plugins/plugin-bench" />);
    await screen.findByRole('link', { name: 'new-db' });
    await act(async () => { finish(response({ instances: [pg] })); });
    expect(screen.queryByRole('link', { name: 'postgres' })).toBeNull();
    expect(screen.getByRole('link', { name: 'new-db' })).toBeTruthy();
  });

  it('does not discover instances when host context is invalid', () => {
    const fetch = fetchFor();
    render(<BenchmarkPage pluginFetch={fetch} basePath="invalid" />);
    expect(screen.getByRole('alert').textContent).toContain('current Kubernetes cluster');
    expect(fetch).not.toHaveBeenCalled();
  });
});

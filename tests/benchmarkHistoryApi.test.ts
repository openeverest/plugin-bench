import { describe, expect, it, vi } from 'vitest';
import type { PluginApi } from '@openeverest/plugin-sdk';
import { BenchmarkHistoryError, listBenchmarkRuns } from '../src/benchmarkHistoryApi';
import { getBenchmarkServiceStatus } from '../src/benchmarkDriversApi';

const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const target = { k8sCluster: 'team/a', namespace: 'dbs', instance: 'pg 1' };
const entry = (extra: Record<string, unknown> = {}) => ({
  id: 'run-1', status: 'succeeded', createdAt: '2026-11-01T10:00:00Z', completedAt: '2026-11-01T10:01:00Z',
  outputTruncated: false, databaseType: 'postgresql', driver: 'pgbench', runnerImage: 'img:1',
  options: { clients: 4 }, metrics: [{ name: 'tps', unit: 'tx/s', value: 1234.5 }], ...extra,
});

describe('benchmark history API', () => {
  it('requests one encoded target page and parses validated entries', async () => {
    const fetch = vi.fn<PluginApi['fetch']>(async () => response({ runs: [entry(), entry({ id: 'run-0', status: 'running', completedAt: undefined, metrics: undefined })] }));
    const runs = await listBenchmarkRuns(fetch, target, { limit: 5 });
    expect(fetch).toHaveBeenCalledWith('/api/runs?k8sCluster=team%2Fa&namespace=dbs&instance=pg+1&limit=5',
      expect.objectContaining({ method: 'GET' }));
    expect(runs.map(run => run.id)).toEqual(['run-1', 'run-0']);
    expect(runs[0]).toMatchObject({ databaseType: 'postgresql', driver: 'pgbench', metrics: [{ name: 'tps', unit: 'tx/s', value: 1234.5 }] });
    expect(runs[1].metrics).toBeUndefined();
  });

  it.each([
    { name: 'missing runs', body: {} },
    { name: 'non-array runs', body: { runs: {} } },
    { name: 'blank ID', body: { runs: [entry({ id: ' ' })] } },
    { name: 'failed without error', body: { runs: [entry({ status: 'failed' })] } },
    { name: 'one malformed entry among valid ones', body: { runs: [entry(), entry({ id: 'bad', createdAt: 'not a date' })] } },
  ])('rejects the whole page for $name', async ({ body }) => {
    const fetch = vi.fn<PluginApi['fetch']>(async () => response(body));
    await expect(listBenchmarkRuns(fetch, target)).rejects.toThrow('invalid history');
  });

  it('reports HTTP failures and hides transport details', async () => {
    await expect(listBenchmarkRuns(async () => response({ error: 'x' }, 403), target)).rejects.toThrow('HTTP 403');
    const failure = listBenchmarkRuns(async () => { throw new TypeError('socket-detail'); }, target);
    await expect(failure).rejects.toBeInstanceOf(BenchmarkHistoryError);
    await expect(failure).rejects.not.toThrow('socket-detail');
  });

  it('aborts with the caller', async () => {
    const controller = new AbortController();
    const pending = listBenchmarkRuns(() => new Promise<Response>(() => {}), target, { signal: controller.signal });
    controller.abort();
    await expect(pending).rejects.toMatchObject({ name: 'AbortError' });
  });
});

describe('benchmark service status', () => {
  it.each([
    { storage: 'postgresql', persistent: true },
    { storage: 'ephemeral', persistent: false },
    { storage: undefined, persistent: false },
  ])('reports persistent history only for PostgreSQL storage ($storage)', async ({ storage, persistent }) => {
    const status = await getBenchmarkServiceStatus(async () => response({ drivers: ['pgbench'], storage }));
    expect(status).toEqual({ drivers: ['pgbench'], persistentHistory: persistent });
  });
});

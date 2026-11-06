import type { PluginApi } from '@openeverest/plugin-sdk';
import { parseBenchmarkRun } from './benchmarkApi.js';
import type { BenchmarkRun } from './benchmarkApi';
import type { BenchmarkTarget } from './benchmarkTarget';
import { withRequestTimeout } from './requestTimeout.js';

/** History entries omit runner output; fetch a single run for its logs. */
export type BenchmarkRunSummary = Omit<BenchmarkRun, 'output'>;

export const BENCHMARK_HISTORY_LIMIT = 20;

export class BenchmarkHistoryError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'BenchmarkHistoryError';
  }
}

export async function listBenchmarkRuns(
  pluginFetch: PluginApi['fetch'],
  target: BenchmarkTarget,
  { signal, timeoutMs = 30_000, limit = BENCHMARK_HISTORY_LIMIT }: { signal?: AbortSignal; timeoutMs?: number; limit?: number } = {},
): Promise<BenchmarkRunSummary[]> {
  return withRequestTimeout(signal, timeoutMs,
    timedOut => timedOut
      ? new BenchmarkHistoryError('Loading benchmark history timed out.')
      : new DOMException('The history request was interrupted.', 'AbortError'),
    requestSignal => sendListBenchmarkRuns(pluginFetch, target, limit, requestSignal),
  );
}

async function sendListBenchmarkRuns(
  pluginFetch: PluginApi['fetch'], target: BenchmarkTarget, limit: number, signal: AbortSignal,
): Promise<BenchmarkRunSummary[]> {
  const query = new URLSearchParams({
    k8sCluster: target.k8sCluster, namespace: target.namespace, instance: target.instance, limit: String(limit),
  });
  try {
    const response = await pluginFetch(`/api/runs?${query}`, { method: 'GET', signal });
    if (response.status !== 200) {
      throw new BenchmarkHistoryError(`Could not load benchmark history (HTTP ${response.status}).`);
    }
    const body: unknown = await response.json();
    if (typeof body !== 'object' || body === null || !('runs' in body) || !Array.isArray(body.runs)) {
      throw new BenchmarkHistoryError('The benchmark service returned an invalid history.');
    }
    const runs: BenchmarkRunSummary[] = [];
    // Reject the whole page on any malformed entry rather than silently
    // showing a history with gaps.
    for (const entry of body.runs) {
      const run = parseBenchmarkRun(entry);
      if (!run) throw new BenchmarkHistoryError('The benchmark service returned an invalid history.');
      runs.push(run);
    }
    return runs;
  } catch (error) {
    if (error instanceof BenchmarkHistoryError) throw error;
    throw new BenchmarkHistoryError('Could not load benchmark history.');
  }
}

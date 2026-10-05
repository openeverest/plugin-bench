import type { PluginApi } from '@openeverest/plugin-sdk';
import { BenchmarkStatusError, getBenchmarkRun } from './benchmarkApi.js';
import type { BenchmarkRun } from './benchmarkApi';

export type PollingFeedback = {
  error: BenchmarkStatusError;
  retryInMs?: number;
  paused: boolean;
};

type PollingOptions = {
  onRun: (run: BenchmarkRun) => void;
  onError: (feedback: PollingFeedback) => void;
  fetchRun?: typeof getBenchmarkRun;
  schedule?: (callback: () => void, delay: number) => ReturnType<typeof setTimeout>;
  clearScheduled?: (timer: ReturnType<typeof setTimeout>) => void;
};

// Each session tracks one ID. Stop it before replacing it with another session.
export function startBenchmarkRunPolling(
  pluginFetch: PluginApi['fetch'],
  id: string,
  { onRun, onError, fetchRun = getBenchmarkRun,
    schedule = setTimeout, clearScheduled = clearTimeout }: PollingOptions
): () => void {
  let active = true;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let controller: AbortController | undefined;
  let failures = 0;
  const retryDelays = [2_000, 4_000, 8_000];

  const stop = () => {
    active = false;
    if (timer !== undefined) clearScheduled(timer);
    timer = undefined;
    controller?.abort();
    controller = undefined;
  };

  const enqueue = (delay: number) => {
    if (active) timer = schedule(() => { timer = undefined; void poll(); }, delay);
  };

  async function poll() {
    if (!active) return;
    controller = new AbortController();
    let run: BenchmarkRun;
    try {
      run = await fetchRun(pluginFetch, id, { signal: controller.signal });
    } catch (cause) {
      if (!active) return;
      controller = undefined;
      if (cause instanceof Error && cause.name === 'AbortError') {
        stop();
        return;
      }
      const error = cause instanceof BenchmarkStatusError ? cause
        : new BenchmarkStatusError('Could not retrieve the benchmark status.');
      const delay = error.retryable ? retryDelays[failures++] : undefined;
      if (delay === undefined) {
        stop();
        onError({ error, paused: true });
      } else {
        onError({ error, retryInMs: delay, paused: false });
        enqueue(delay);
      }
      return;
    }
    if (!active) return;
    controller = undefined;
    failures = 0;
    if (run.status !== 'running') stop();
    onRun(run);
    if (run.status === 'running') enqueue(2_000);
  }

  void poll();
  return stop;
}

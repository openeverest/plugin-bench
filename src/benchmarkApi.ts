import type { PluginApi } from '@openeverest/plugin-sdk';
import type { BenchmarkTarget } from './benchmarkTarget';

export type CreateBenchmarkRunRequest = {
  target: BenchmarkTarget;
  database: string;
  durationSeconds: number;
  clients: number;
  threads: number;
  scale: number;
  initialize: boolean;
};

export type CreateBenchmarkRunResponse = {
  id: string;
  status: 'running';
};

export type BenchmarkRunStatus = 'running' | 'succeeded' | 'failed';

export type BenchmarkRun = {
  id: string;
  status: BenchmarkRunStatus;
  createdAt: string;
  completedAt?: string;
  jobName?: string;
  output?: string;
  outputTruncated: boolean;
  error?: string;
};

export class BenchmarkStatusError extends Error {
  constructor(message: string, readonly status?: number, readonly retryable = false) {
    super(message);
    this.name = 'BenchmarkStatusError';
  }
}

export async function getBenchmarkRun(
  pluginFetch: PluginApi['fetch'],
  id: string,
  { signal, timeoutMs = 30_000 }: { signal?: AbortSignal; timeoutMs?: number } = {}
): Promise<BenchmarkRun> {
  const controller = new AbortController();
  let timedOut = false;
  const aborted = new Promise<never>((_, reject) => {
    controller.signal.addEventListener('abort', () => {
      reject(timedOut
        ? new BenchmarkStatusError('The status request timed out.', undefined, true)
        : new DOMException('The status request was interrupted.', 'AbortError'));
    }, { once: true });
  });
  const cancel = () => controller.abort();
  signal?.addEventListener('abort', cancel, { once: true });
  const timer = setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, timeoutMs);

  try {
    if (signal?.aborted) {
      controller.abort();
      return await aborted;
    }
    return await Promise.race([sendGetBenchmarkRun(pluginFetch, id, controller.signal), aborted]);
  } finally {
    clearTimeout(timer);
    signal?.removeEventListener('abort', cancel);
  }
}

async function sendGetBenchmarkRun(
  pluginFetch: PluginApi['fetch'], id: string, signal: AbortSignal
): Promise<BenchmarkRun> {
  let response: Response;
  try {
    response = await pluginFetch(`/api/runs/${encodeURIComponent(id)}`, { method: 'GET', signal });
  } catch {
    throw new BenchmarkStatusError('Could not retrieve the benchmark status.', undefined, true);
  }
  if (!response.ok) {
    throw new BenchmarkStatusError(await readErrorMessage(response), response.status,
      response.status === 429 || response.status >= 500);
  }
  if (response.status !== 200) {
    throw new BenchmarkStatusError('The status service returned an unexpected response.', response.status);
  }
  try {
    const body: unknown = await response.json();
    if (isBenchmarkRun(body, id)) return body;
  } catch {
    // Invalid JSON and invalid shapes share the fallback below.
  }
  throw new BenchmarkStatusError('The status service returned an invalid response.', response.status);
}

function isBenchmarkRun(value: unknown, id: string): value is BenchmarkRun {
  if (typeof value !== 'object' || value === null) return false;
  const run = value as Record<string, unknown>;
  const timestamp = (value: unknown) => typeof value === 'string' &&
    value.trim() !== '' && Number.isFinite(Date.parse(value));
  return run.id === id &&
    (run.status === 'running' || run.status === 'succeeded' || run.status === 'failed') &&
    timestamp(run.createdAt) && typeof run.outputTruncated === 'boolean' &&
    (run.completedAt === undefined || timestamp(run.completedAt)) &&
    (run.status === 'running' || timestamp(run.completedAt)) &&
    ['jobName', 'output', 'error'].every(key => run[key] === undefined || typeof run[key] === 'string') &&
    (run.status !== 'failed' || (typeof run.error === 'string' && run.error.trim() !== ''));
}

export class BenchmarkApiError extends Error {
  constructor(
    message: string,
    readonly status?: number,
    readonly outcomeUnknown = false
  ) {
    super(message);
    this.name = 'BenchmarkApiError';
  }
}

export async function createBenchmarkRun(
  pluginFetch: PluginApi['fetch'],
  request: CreateBenchmarkRunRequest,
  { signal, timeoutMs = 30_000 }: { signal?: AbortSignal; timeoutMs?: number } = {}
): Promise<CreateBenchmarkRunResponse> {
  const controller = new AbortController();
  let timedOut = false;
  const aborted = new Promise<never>((_, reject) => {
    controller.signal.addEventListener('abort', () => {
      reject(unknownOutcome(timedOut ? 'The submission timed out.' : 'The submission was interrupted.'));
    }, { once: true });
  });
  const cancel = () => controller.abort();
  signal?.addEventListener('abort', cancel, { once: true });
  const timer = setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, timeoutMs);

  try {
    if (signal?.aborted) {
      controller.abort();
      return await aborted;
    }
    // Bound both fetching and reading the body, even if the host ignores abort.
    return await Promise.race([
      sendCreateBenchmarkRun(pluginFetch, request, controller.signal),
      aborted,
    ]);
  } finally {
    clearTimeout(timer);
    signal?.removeEventListener('abort', cancel);
  }
}

function unknownOutcome(reason: string, status?: number): BenchmarkApiError {
  return new BenchmarkApiError(
    `${reason} Could not confirm whether the benchmark started. It may already be running; check the instance before submitting again.`,
    status,
    true
  );
}

async function sendCreateBenchmarkRun(
  pluginFetch: PluginApi['fetch'],
  request: CreateBenchmarkRunRequest,
  signal: AbortSignal
): Promise<CreateBenchmarkRunResponse> {
  let response: Response;
  try {
    response = await pluginFetch('/api/runs', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(request),
      signal,
    });
  } catch {
    throw unknownOutcome('The connection to the benchmark service was lost.');
  }

  if (!response.ok) {
    const message = await readErrorMessage(response);
    if (response.status >= 500) {
      throw unknownOutcome(message, response.status);
    }
    throw new BenchmarkApiError(message, response.status);
  }

  if (response.status !== 202) {
    throw unknownOutcome('The benchmark service returned an unexpected response.', response.status);
  }

  try {
    const body: unknown = await response.json();
    if (
      typeof body === 'object' &&
      body !== null &&
      'id' in body &&
      typeof body.id === 'string' &&
      body.id.trim() !== '' &&
      'status' in body &&
      body.status === 'running'
    ) {
      return { id: body.id, status: 'running' };
    }
  } catch {
    // Use the same safe message for invalid JSON and an unexpected response shape.
  }

  throw unknownOutcome('The benchmark service returned an invalid response.', response.status);
}

async function readErrorMessage(response: Response): Promise<string> {
  try {
    const body: unknown = await response.json();
    if (
      typeof body === 'object' &&
      body !== null &&
      'error' in body &&
      typeof body.error === 'string' &&
      body.error.trim() !== ''
    ) {
      return body.error;
    }
  } catch {
    // Fall back when the server response is not valid JSON.
  }

  return `The benchmark service returned HTTP ${response.status}.`;
}

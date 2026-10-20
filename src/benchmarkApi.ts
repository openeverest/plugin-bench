import type { PluginApi } from '@openeverest/plugin-sdk';
import type { BenchmarkTarget } from './benchmarkTarget';
import type { DriverRequest } from './drivers';
import { isNodeAffinity } from './benchmarkNodeAffinity.js';
import type { NodeAffinity } from './benchmarkNodeAffinity';

export type BenchmarkRunResources = {
  cpuRequest?: string;
  cpuLimit?: string;
  memoryRequest?: string;
  memoryLimit?: string;
};

export type CreateBenchmarkRunRequest = DriverRequest & {
  target: BenchmarkTarget;
  /** Omit to benchmark the instance's default database. */
  database?: string;
  resources?: BenchmarkRunResources;
  nodeAffinity?: NodeAffinity;
};

export type CreateBenchmarkRunResponse = {
  id: string;
  status: 'running';
};

export type BenchmarkRunStatus = 'running' | 'succeeded' | 'failed';

export type BenchmarkMetric = {
  name: string;
  unit: string;
  value: number;
};

type BenchmarkRunCore = {
  id: string;
  status: BenchmarkRunStatus;
  database?: string;
  resources?: BenchmarkRunResources;
  createdAt: string;
  /** Absent on older backends; an empty object means no configured affinity. */
  nodeAffinity?: NodeAffinity;
  completedAt?: string;
  jobName?: string;
  output?: string;
  outputTruncated: boolean;
  error?: string;
};

export type BenchmarkRun = BenchmarkRunCore & {
  /** Optional for runs created before driver snapshots were available. */
  driver?: string;
  runnerImage?: string;
  /** Effective driver options; their keys and values belong to the driver. */
  options?: Record<string, unknown>;
  /** Absent when structured metrics are unavailable or invalid. */
  metrics?: BenchmarkMetric[];
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
    const run = parseBenchmarkRun(body, id);
    if (run) return run;
  } catch {
    // Invalid JSON and invalid shapes share the fallback below.
  }
  throw new BenchmarkStatusError('The status service returned an invalid response.', response.status);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isBenchmarkRunCore(value: unknown, id: string): value is BenchmarkRunCore & Record<string, unknown> {
  if (!isRecord(value)) return false;
  const run = value;
  const timestamp = (value: unknown) => typeof value === 'string' &&
    value.trim() !== '' && Number.isFinite(Date.parse(value));
  return run.id === id &&
    (run.status === 'running' || run.status === 'succeeded' || run.status === 'failed') &&
    timestamp(run.createdAt) && typeof run.outputTruncated === 'boolean' &&
    (run.nodeAffinity === undefined || isNodeAffinity(run.nodeAffinity)) &&
    (run.completedAt === undefined || timestamp(run.completedAt)) &&
    (run.status === 'running' || timestamp(run.completedAt)) &&
    (run.resources === undefined || isBenchmarkRunResources(run.resources)) &&
    ['database', 'jobName', 'output', 'error'].every(key => run[key] === undefined || typeof run[key] === 'string') &&
    (run.status !== 'failed' || (typeof run.error === 'string' && run.error.trim() !== ''));
}

function parseBenchmarkRun(value: unknown, id: string): BenchmarkRun | undefined {
  if (!isBenchmarkRunCore(value, id)) return undefined;
  // Construct the known fields explicitly: optional metadata must be checked
  // before reaching result components, even when the core run is valid.
  const run: BenchmarkRun = {
    id: value.id,
    status: value.status,
    createdAt: value.createdAt,
    outputTruncated: value.outputTruncated,
  };
  for (const key of ['database', 'completedAt', 'jobName', 'output', 'error'] as const) {
    if (value[key] !== undefined) run[key] = value[key];
  }
  if (value.resources !== undefined) run.resources = value.resources;
  if (value.nodeAffinity !== undefined) run.nodeAffinity = value.nodeAffinity;
  for (const key of ['driver', 'runnerImage'] as const) {
    const field = value[key];
    if (typeof field === 'string' && field.trim() !== '') run[key] = field;
  }
  if (isRecord(value.options)) run.options = value.options;
  const metrics = parseMetrics(value.metrics);
  if (metrics) run.metrics = metrics;
  return run;
}

function parseMetrics(value: unknown): BenchmarkMetric[] | undefined {
  if (!Array.isArray(value) || value.length === 0) return undefined;
  const metrics: BenchmarkMetric[] = [];
  const seen = new Set<string>();
  // Reject the entire set when any entry is malformed; showing a partial set
  // could imply that the runner reported fewer measurements than it did.
  for (const metric of value) {
    if (!isRecord(metric) || typeof metric.name !== 'string' || metric.name.trim() === '' ||
        typeof metric.unit !== 'string' || metric.unit.trim() === '' ||
        typeof metric.value !== 'number' || !Number.isFinite(metric.value)) return undefined;
    const key = JSON.stringify([metric.name, metric.unit]);
    if (seen.has(key)) return undefined;
    seen.add(key);
    metrics.push({ name: metric.name, unit: metric.unit, value: metric.value });
  }
  return metrics;
}

function isBenchmarkRunResources(value: unknown): value is BenchmarkRunResources {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return false;
  const resources = value as Record<string, unknown>;
  return ['cpuRequest', 'cpuLimit', 'memoryRequest', 'memoryLimit']
    .every(key => resources[key] === undefined || typeof resources[key] === 'string');
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

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

export class BenchmarkApiError extends Error {
  constructor(
    message: string,
    readonly status?: number
  ) {
    super(message);
    this.name = 'BenchmarkApiError';
  }
}

export async function createBenchmarkRun(
  pluginFetch: PluginApi['fetch'],
  request: CreateBenchmarkRunRequest
): Promise<CreateBenchmarkRunResponse> {
  let response: Response;
  try {
    response = await pluginFetch('/api/runs', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(request),
    });
  } catch {
    throw new BenchmarkApiError('Could not connect to the benchmark service. Please try again.');
  }

  if (!response.ok) {
    throw new BenchmarkApiError(await readErrorMessage(response), response.status);
  }

  if (response.status !== 202) {
    throw new BenchmarkApiError('The benchmark service returned an unexpected response.', response.status);
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

  throw new BenchmarkApiError('The benchmark service returned an invalid response.', response.status);
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

  return `Could not start the benchmark (HTTP ${response.status}).`;
}

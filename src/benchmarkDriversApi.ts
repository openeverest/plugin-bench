import type { PluginApi } from '@openeverest/plugin-sdk';
import { withRequestTimeout } from './requestTimeout.js';

export class BenchmarkDriversError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'BenchmarkDriversError';
  }
}

export type BenchmarkServiceStatus = {
  drivers: string[];
  /** False when history is held in backend memory and lost on restart. */
  persistentHistory: boolean;
};

export async function getBenchmarkServiceStatus(
  pluginFetch: PluginApi['fetch'],
  { signal, timeoutMs = 30_000 }: { signal?: AbortSignal; timeoutMs?: number } = {},
): Promise<BenchmarkServiceStatus> {
  return withRequestTimeout(signal, timeoutMs,
    timedOut => timedOut
      ? new BenchmarkDriversError('Checking available benchmark drivers timed out.')
      : new DOMException('The driver request was interrupted.', 'AbortError'),
    requestSignal => sendGetBenchmarkServiceStatus(pluginFetch, requestSignal),
  );
}

export async function getEnabledBenchmarkDrivers(
  pluginFetch: PluginApi['fetch'],
  options: { signal?: AbortSignal; timeoutMs?: number } = {},
): Promise<string[]> {
  return (await getBenchmarkServiceStatus(pluginFetch, options)).drivers;
}

async function sendGetBenchmarkServiceStatus(
  pluginFetch: PluginApi['fetch'], signal: AbortSignal,
): Promise<BenchmarkServiceStatus> {
  try {
    const response = await pluginFetch('/api/status', { method: 'GET', signal });
    if (response.status !== 200) {
      throw new BenchmarkDriversError(`Could not check available benchmark drivers (HTTP ${response.status}).`);
    }
    const body: unknown = await response.json();
    if (typeof body !== 'object' || body === null || !('drivers' in body) ||
        !Array.isArray(body.drivers) ||
        !body.drivers.every((id: unknown) => typeof id === 'string' && id.trim() !== '')) {
      throw new BenchmarkDriversError('The benchmark service returned an invalid driver list.');
    }
    return {
      drivers: [...new Set(body.drivers as string[])],
      persistentHistory: 'storage' in body && body.storage === 'postgresql',
    };
  } catch (error) {
    if (error instanceof BenchmarkDriversError) throw error;
    throw new BenchmarkDriversError('Could not check available benchmark drivers.');
  }
}

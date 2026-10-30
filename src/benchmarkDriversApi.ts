import type { PluginApi } from '@openeverest/plugin-sdk';
import { withRequestTimeout } from './requestTimeout.js';

export class BenchmarkDriversError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'BenchmarkDriversError';
  }
}

export async function getEnabledBenchmarkDrivers(
  pluginFetch: PluginApi['fetch'],
  { signal, timeoutMs = 30_000 }: { signal?: AbortSignal; timeoutMs?: number } = {},
): Promise<string[]> {
  return withRequestTimeout(signal, timeoutMs,
    timedOut => timedOut
      ? new BenchmarkDriversError('Checking available benchmark drivers timed out.')
      : new DOMException('The driver request was interrupted.', 'AbortError'),
    requestSignal => sendGetEnabledBenchmarkDrivers(pluginFetch, requestSignal),
  );
}

async function sendGetEnabledBenchmarkDrivers(
  pluginFetch: PluginApi['fetch'], signal: AbortSignal,
): Promise<string[]> {
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
    return [...new Set(body.drivers as string[])];
  } catch (error) {
    if (error instanceof BenchmarkDriversError) throw error;
    throw new BenchmarkDriversError('Could not check available benchmark drivers.');
  }
}

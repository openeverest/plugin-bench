import type { PluginApi } from '@openeverest/plugin-sdk';
import { BenchmarkStatusError } from './benchmarkApi';

export async function getEnabledBenchmarkDrivers(
  pluginFetch: PluginApi['fetch'],
  { signal, timeoutMs = 30_000 }: { signal?: AbortSignal; timeoutMs?: number } = {},
): Promise<string[]> {
  const controller = new AbortController();
  let timedOut = false;
  const aborted = new Promise<never>((_, reject) => {
    controller.signal.addEventListener('abort', () => reject(timedOut
      ? new BenchmarkStatusError('Checking available benchmark drivers timed out.')
      : new DOMException('The driver request was interrupted.', 'AbortError')), { once: true });
  });
  const cancel = () => controller.abort();
  signal?.addEventListener('abort', cancel, { once: true });
  const timer = setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, timeoutMs);

  try {
    if (signal?.aborted) controller.abort();
    return await Promise.race([aborted, (async () => {
      if (controller.signal.aborted) return await aborted;
      const response = await pluginFetch('/api/status', { method: 'GET', signal: controller.signal });
      if (response.status !== 200) {
        throw new BenchmarkStatusError(`Could not check available benchmark drivers (HTTP ${response.status}).`);
      }
      const body: unknown = await response.json();
      if (typeof body !== 'object' || body === null || !('drivers' in body) ||
          !Array.isArray(body.drivers) ||
          !body.drivers.every((id: unknown) => typeof id === 'string' && id.trim() !== '')) {
        throw new BenchmarkStatusError('The benchmark service returned an invalid driver list.');
      }
      return [...new Set(body.drivers as string[])];
    })()]);
  } catch (error) {
    if (controller.signal.aborted || error instanceof BenchmarkStatusError) throw error;
    throw new BenchmarkStatusError('Could not check available benchmark drivers.');
  } finally {
    clearTimeout(timer);
    signal?.removeEventListener('abort', cancel);
  }
}

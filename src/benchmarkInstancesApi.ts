import type { PluginApi } from '@openeverest/plugin-sdk';
import { withRequestTimeout } from './requestTimeout';

export type BenchmarkInstance = {
  k8sCluster: string;
  namespace: string;
  name: string;
  provider: string;
};

export class BenchmarkInstancesError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'BenchmarkInstancesError';
  }
}

function isInstance(value: unknown): value is BenchmarkInstance {
  if (typeof value !== 'object' || value === null) return false;
  return ['k8sCluster', 'namespace', 'name', 'provider'].every(key =>
    key in value && typeof (value as Record<string, unknown>)[key] === 'string' &&
    ((value as Record<string, unknown>)[key] as string).trim() !== '',
  );
}

export async function getBenchmarkInstances(
  pluginFetch: PluginApi['fetch'],
  { k8sCluster, signal, timeoutMs = 30_000 }: {
    k8sCluster: string;
    signal?: AbortSignal;
    timeoutMs?: number;
  },
): Promise<BenchmarkInstance[]> {
  if (!k8sCluster.trim()) throw new BenchmarkInstancesError('A Kubernetes cluster is required.');
  return withRequestTimeout(signal, timeoutMs,
    timedOut => timedOut
      ? new BenchmarkInstancesError('Discovering database instances timed out.')
      : new DOMException('Instance discovery was interrupted.', 'AbortError'),
    async requestSignal => {
      try {
        const response = await pluginFetch(`/api/instances?${new URLSearchParams({ k8sCluster })}`, {
          method: 'GET', signal: requestSignal,
        });
        if (response.status !== 200) {
          throw new BenchmarkInstancesError(`Could not discover database instances (HTTP ${response.status}).`);
        }
        const body: unknown = await response.json();
        if (typeof body !== 'object' || body === null || !('instances' in body) ||
            !Array.isArray(body.instances) || !body.instances.every(isInstance) ||
            body.instances.some(instance => instance.k8sCluster !== k8sCluster)) {
          throw new BenchmarkInstancesError('The benchmark service returned an invalid instance list.');
        }
        return body.instances.map(({ k8sCluster, namespace, name, provider }) =>
          ({ k8sCluster, namespace, name, provider }));
      } catch (error) {
        if (error instanceof BenchmarkInstancesError) throw error;
        throw new BenchmarkInstancesError('Could not discover database instances.');
      }
    },
  );
}

import type { BenchmarkInstance } from './benchmarkInstancesApi';
import { getBenchmarkDriversForProvider } from './drivers';
import type { BenchmarkDriverId } from './drivers';

export function benchmarkInstanceKey(instance: BenchmarkInstance): string {
  return JSON.stringify([instance.k8sCluster, instance.namespace, instance.name]);
}

export function enabledDriversForInstance(
  instance: BenchmarkInstance, enabledDrivers: readonly string[],
): BenchmarkDriverId[] {
  return getBenchmarkDriversForProvider(instance.provider).filter(driver => enabledDrivers.includes(driver));
}

export function filterBenchmarkInstances(
  instances: readonly BenchmarkInstance[], enabledDrivers: readonly string[],
): BenchmarkInstance[] {
  const seen = new Set<string>();
  return instances.filter(instance => {
    if (enabledDriversForInstance(instance, enabledDrivers).length === 0) return false;
    const key = benchmarkInstanceKey(instance);
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

export function benchmarkClusterFromBasePath(basePath: string): string {
  const match = /^\/v1\/clusters\/([^/?#]+)\/plugins\/([^/?#]+)\/?$/.exec(basePath);
  if (!match) throw new Error('Invalid plugin base path.');
  try {
    const cluster = decodeURIComponent(match[1]);
    const plugin = decodeURIComponent(match[2]);
    if (!cluster.trim() || !plugin.trim()) throw new Error();
    return cluster;
  } catch {
    throw new Error('Invalid plugin base path.');
  }
}

function routeSegment(value: string): string {
  // Dot segments are normalized by browsers even when percent-encoded.
  if (!value.trim() || value === '.' || value === '..') throw new Error('Invalid instance target.');
  return encodeURIComponent(value);
}

export function benchmarkInstanceHref(instance: BenchmarkInstance, currentCluster: string): string {
  if (!currentCluster.trim() || instance.k8sCluster !== currentCluster) {
    throw new Error('Cannot navigate to an instance in another Kubernetes cluster.');
  }
  return `/databases/${routeSegment(instance.namespace)}/${routeSegment(instance.name)}/performance-benchmark`;
}

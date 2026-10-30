import type { ClusterDetailTabProps } from '@openeverest/plugin-sdk';

export type BenchmarkTarget = {
  k8sCluster: string;
  namespace: string;
  instance: string;
};

type InstanceClusterContext = {
  clusterName?: unknown;
  spec?: { clusterName?: unknown };
};

function nonEmptyString(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined;
  const trimmed = value.trim();
  return trimmed ? trimmed : undefined;
}

function record(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined;
}

export function providerFromClusterDetailProps(
  props: Pick<ClusterDetailTabProps, 'cluster'>
): string | undefined {
  const instance = record(props.cluster);
  const spec = record(instance?.spec);
  const providerRef = record(spec?.providerRef);
  return nonEmptyString(providerRef?.name);
}

export function targetFromClusterDetailProps(
  props: Pick<ClusterDetailTabProps, 'cluster' | 'namespace' | 'instanceName'>
): BenchmarkTarget {
  const instance = (props.cluster && typeof props.cluster === 'object'
    ? props.cluster
    : {}) as InstanceClusterContext;

  return {
    k8sCluster:
      nonEmptyString(instance.clusterName) ??
      nonEmptyString(instance.spec?.clusterName) ??
      'main',
    namespace: props.namespace,
    instance: props.instanceName,
  };
}

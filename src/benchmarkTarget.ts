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

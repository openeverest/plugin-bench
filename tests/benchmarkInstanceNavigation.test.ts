import { describe, expect, it } from 'vitest';
import { benchmarkClusterFromBasePath, benchmarkInstanceHref, benchmarkInstanceKey, enabledDriversForInstance, filterBenchmarkInstances } from '../src/benchmarkInstanceNavigation';

const pg = { k8sCluster: 'main', namespace: 'default', name: 'db', provider: 'provider-percona-postgresql' };
const mysql = { ...pg, name: 'mysql', provider: 'percona-server-mysql' };
const pxc = { ...mysql, name: 'pxc', provider: 'percona-xtradb-cluster' };

describe('instance filtering and identity', () => {
  it('intersects supported providers with enabled drivers', () => {
    const instances = [pg, mysql, pxc, { ...pg, provider: 'unknown' }];
    expect(filterBenchmarkInstances(instances, ['pgbench', 'unknown'])).toEqual([pg]);
    expect(filterBenchmarkInstances(instances, ['sysbench'])).toEqual([mysql, pxc]);
    expect(filterBenchmarkInstances(instances, [])).toEqual([]);
    expect(filterBenchmarkInstances(instances, ['unknown'])).toEqual([]);
  });

  it('reports only the enabled drivers that support an instance provider', () => {
    expect(enabledDriversForInstance(pg, ['pgbench', 'sysbench'])).toEqual(['pgbench']);
    expect(enabledDriversForInstance(pxc, ['pgbench', 'sysbench'])).toEqual(['sysbench']);
    expect(enabledDriversForInstance(pxc, ['pgbench'])).toEqual([]);
  });

  it('deduplicates full targets while preserving namespace and cluster distinctions', () => {
    const otherNamespace = { ...pg, namespace: 'other' };
    const otherCluster = { ...pg, k8sCluster: 'other' };
    const instances = [pg, { ...pg }, otherNamespace, otherCluster];
    expect(filterBenchmarkInstances(instances, ['pgbench'])).toEqual([pg, otherNamespace, otherCluster]);
    expect(new Set([pg, otherNamespace, otherCluster].map(benchmarkInstanceKey)).size).toBe(3);
    expect(instances).toHaveLength(4);
    expect(benchmarkInstanceKey({ ...pg, namespace: 'a/b', name: 'c' }))
      .not.toBe(benchmarkInstanceKey({ ...pg, namespace: 'a', name: 'b/c' }));
  });
});

describe('instance navigation', () => {
  it('extracts and decodes the SDK cluster', () => {
    expect(benchmarkClusterFromBasePath('/v1/clusters/main/plugins/plugin-bench')).toBe('main');
    expect(benchmarkClusterFromBasePath('/v1/clusters/team%2Fa/plugins/plugin-bench/')).toBe('team/a');
  });

  it.each(['', '/v1/clusters/main', '/v1/clusters//plugins/bench',
    '/v1/clusters/%20/plugins/bench', '/v1/clusters/%ZZ/plugins/bench',
    '/v1/clusters/main/plugins/%ZZ', '/v1/clusters/main/plugins/bench/extra',
    '/v1/clusters/main/plugins/bench?x=1', 'https://host/v1/clusters/main/plugins/bench',
  ])('rejects invalid base path %s', path => {
    expect(() => benchmarkClusterFromBasePath(path)).toThrow('Invalid plugin base path');
  });

  it('links to the existing benchmark tab and encodes each route segment', () => {
    expect(benchmarkInstanceHref(pg, 'main')).toBe('/databases/default/db/performance-benchmark');
    expect(benchmarkInstanceHref({ ...pg, namespace: 'a/b', name: 'db ?#%' }, 'main'))
      .toBe('/databases/a%2Fb/db%20%3F%23%25/performance-benchmark');
  });

  it('refuses cross-cluster links', () => {
    expect(() => benchmarkInstanceHref(pg, 'other')).toThrow('another Kubernetes cluster');
  });

  it.each(['', ' ', '.', '..'])('refuses unusable route segments %s', value => {
    expect(() => benchmarkInstanceHref({ ...pg, namespace: value }, 'main')).toThrow();
    expect(() => benchmarkInstanceHref({ ...pg, name: value }, 'main')).toThrow();
  });
});

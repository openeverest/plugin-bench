import { describe, expect, it } from 'vitest';
import { providerFromClusterDetailProps, targetFromClusterDetailProps } from '../src/benchmarkTarget';

describe('benchmark instance context', () => {
  it('reads and trims the registered provider reference from the Instance', () => {
    expect(providerFromClusterDetailProps({
      cluster: { spec: { providerRef: { name: ' percona-server-mysql ' } } },
    })).toBe('percona-server-mysql');
  });

  it.each([
    undefined, null, false, 42, 'mysql', [], {}, { provider: 'mysql' },
    { spec: null }, { spec: 'mysql' }, { spec: [] }, { spec: {} },
    { spec: { providerRef: null } }, { spec: { providerRef: [] } },
    { spec: { providerRef: 'mysql' } }, { spec: { providerRef: {} } },
    { spec: { providerRef: { name: 42 } } }, { spec: { providerRef: { name: [] } } },
    { spec: { providerRef: { name: ' \t ' } } },
  ].map(cluster => ({ cluster })))('tolerates missing or malformed provider context: $cluster', ({ cluster }) => {
    expect(providerFromClusterDetailProps({ cluster })).toBeUndefined();
  });

  it('keeps provider metadata separate from the request target', () => {
    const props = {
      cluster: { clusterName: ' secondary ', spec: { clusterName: 'other', providerRef: { name: 'percona-server-mysql' } } },
      namespace: 'dbs', instanceName: 'mysql-1',
    };
    expect(targetFromClusterDetailProps(props)).toEqual({ k8sCluster: 'secondary', namespace: 'dbs', instance: 'mysql-1' });
    expect(targetFromClusterDetailProps({ ...props, cluster: { spec: { clusterName: 'other' } } })).toEqual({
      k8sCluster: 'other', namespace: 'dbs', instance: 'mysql-1',
    });
    expect(targetFromClusterDetailProps({ ...props, cluster: undefined })).toEqual({
      k8sCluster: 'main', namespace: 'dbs', instance: 'mysql-1',
    });
  });
});

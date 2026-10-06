import type { ClusterDetailTabProps, PluginApi, PluginRegisterFn } from '@openeverest/plugin-sdk';
import { BenchmarkPage } from './BenchmarkPage';
import { BenchmarkTab } from './BenchmarkTab';
import { PluginRoot } from './PluginRoot';

const register: PluginRegisterFn = (api: PluginApi) => {
  const pluginFetch = api.fetch.bind(api);
  const { cssNonce } = api;

  api.registerExtension({
    type: 'sidebarItem',
    label: 'Performance Benchmark',
  });

  api.registerExtension({
    type: 'route',
    label: 'Performance Benchmark',
    component: () => (
      <PluginRoot nonce={cssNonce}>
        <BenchmarkPage />
      </PluginRoot>
    ),
  });

  api.registerExtension({
    type: 'clusterDetailTab',
    label: 'Performance Benchmark',
    path: 'performance-benchmark',
    providers: ['provider-cloudnative-pg', 'provider-percona-postgresql'],
    component: (props: ClusterDetailTabProps) => (
      <PluginRoot nonce={cssNonce}>
        <BenchmarkTab {...props} pluginFetch={pluginFetch} />
      </PluginRoot>
    ),
  });
};

export default register;

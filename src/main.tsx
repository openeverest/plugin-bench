import type {
  ClusterDetailTabProps,
  PluginApi,
  PluginRegisterFn,
  PluginRouteProps,
} from '@openeverest/plugin-sdk';
import { BenchmarkPage, BenchmarkTab } from './BenchmarkTab';

const register: PluginRegisterFn = (api: PluginApi) => {
  const react = api.React;
  const pluginFetch = api.fetch.bind(api);

  api.registerExtension({
    type: 'sidebarItem',
    label: 'Performance Benchmark',
  });

  api.registerExtension({
    type: 'route',
    label: 'Performance Benchmark',
    component: (props: PluginRouteProps) => react.createElement(BenchmarkPage, { ...props, react }),
  });

  api.registerExtension({
    type: 'clusterDetailTab',
    label: 'Performance Benchmark',
    path: 'performance-benchmark',
    providers: ['provider-cloudnative-pg', 'provider-percona-postgresql'],
    component: (props: ClusterDetailTabProps) =>
      react.createElement(BenchmarkTab, { ...props, react, fetch: pluginFetch }),
  });
};

export default register;

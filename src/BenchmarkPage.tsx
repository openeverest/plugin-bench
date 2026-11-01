import { useEffect, useState } from 'react';
import { Alert, Button, CircularProgress, Stack, Typography } from '@mui/material';
import type { PluginApi } from '@openeverest/plugin-sdk';
import { PageHeader } from './PageHeader';
import { BenchmarkInstancesTable } from './BenchmarkInstancesTable';
import { getBenchmarkInstances } from './benchmarkInstancesApi';
import type { BenchmarkInstance } from './benchmarkInstancesApi';
import { getEnabledBenchmarkDrivers } from './benchmarkDriversApi';
import { benchmarkClusterFromBasePath, filterBenchmarkInstances } from './benchmarkInstanceNavigation';

type DiscoveryState =
  | { status: 'loading' }
  | { status: 'ready'; instances: BenchmarkInstance[] }
  | { status: 'failed'; message: string };

export function BenchmarkPage({ pluginFetch, basePath }: {
  pluginFetch: PluginApi['fetch'];
  basePath: string;
}) {
  let cluster: string | undefined;
  try {
    cluster = benchmarkClusterFromBasePath(basePath);
  } catch {
    // Fail closed when the host does not provide a usable cluster context.
  }
  return (
    <Stack spacing={3} sx={{ p: 3, width: '100%', minWidth: 0, boxSizing: 'border-box' }}>
      <PageHeader subtitle="Select a database instance to configure and run a benchmark." />
      {cluster ? <InstanceDiscovery key={cluster} pluginFetch={pluginFetch} cluster={cluster} /> : (
        <Alert severity="error">Could not determine the current Kubernetes cluster. Reload the page to try again.</Alert>
      )}
    </Stack>
  );
}

function InstanceDiscovery({ pluginFetch, cluster }: { pluginFetch: PluginApi['fetch']; cluster: string }) {
  const [state, setState] = useState<DiscoveryState>({ status: 'loading' });
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    setState({ status: 'loading' });
    Promise.all([
      getBenchmarkInstances(pluginFetch, { k8sCluster: cluster, signal: controller.signal }),
      getEnabledBenchmarkDrivers(pluginFetch, { signal: controller.signal }),
    ]).then(([instances, drivers]) => {
      if (!controller.signal.aborted) {
        setState({ status: 'ready', instances: filterBenchmarkInstances(instances, drivers) });
      }
    }).catch(error => {
      if (controller.signal.aborted) return;
      setState({ status: 'failed', message: error instanceof Error ? error.message : 'Could not load database instances.' });
      controller.abort();
    });
    return () => controller.abort();
  }, [pluginFetch, cluster, attempt]);

  if (state.status === 'loading') {
    return <Stack direction="row" spacing={2} alignItems="center" role="status">
      <CircularProgress size={24} aria-label="Loading database instances" />
      <Typography>Loading database instances…</Typography>
    </Stack>;
  }
  if (state.status === 'failed') {
    return <Alert severity="error" action={
      <Button color="inherit" onClick={() => setAttempt(value => value + 1)}>Retry</Button>
    }>{state.message}</Alert>;
  }
  if (state.instances.length === 0) {
    return <Alert severity="info">No accessible database instances are supported by the enabled benchmark drivers in this cluster.</Alert>;
  }
  return <BenchmarkInstancesTable instances={state.instances} currentCluster={cluster} />;
}

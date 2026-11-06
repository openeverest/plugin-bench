import { useEffect, useRef, useState } from 'react';
import { Alert, Box, Button, Stack, Typography } from '@mui/material';
import type { ClusterDetailTabProps, PluginApi } from '@openeverest/plugin-sdk';
import { BenchmarkForm } from './BenchmarkForm';
import { PageHeader } from './PageHeader';
import { RunLookup } from './RunLookup';
import { TrackedBenchmarkRun } from './TrackedBenchmarkRun';
import { BenchmarkApiError, createBenchmarkRun } from './benchmarkApi';
import { toCreateBenchmarkRunRequest } from './benchmarkFormValidation';
import type { BenchmarkFormValues } from './benchmarkFormValidation';
import { providerFromClusterDetailProps, targetFromClusterDetailProps } from './benchmarkTarget';
import type { BenchmarkTarget } from './benchmarkTarget';
import { getBenchmarkDriversForProvider } from './drivers';
import { getBenchmarkServiceStatus } from './benchmarkDriversApi';
import { BenchmarkRunHistory } from './BenchmarkRunHistory';

interface BenchmarkTabProps extends ClusterDetailTabProps {
  pluginFetch: PluginApi['fetch'];
}

type SubmissionState =
  | { status: 'idle' }
  | { status: 'submitting' }
  | { status: 'accepted'; id: string }
  | { status: 'failed'; message: string };

export function BenchmarkTab({ pluginFetch, ...props }: BenchmarkTabProps) {
  const target = targetFromClusterDetailProps(props);
  const provider = providerFromClusterDetailProps(props);
  return (
    <BenchmarkWorkflow
      key={JSON.stringify([target.k8sCluster, target.namespace, target.instance, provider])}
      pluginFetch={pluginFetch}
      target={target}
      provider={provider}
    />
  );
}

interface BenchmarkWorkflowProps {
  pluginFetch: PluginApi['fetch'];
  target: BenchmarkTarget;
  provider: string | undefined;
}

function BenchmarkWorkflow({ pluginFetch, target, provider }: BenchmarkWorkflowProps) {
  const supportedDrivers = getBenchmarkDriversForProvider(provider);
  const [driverState, setDriverState] = useState<
    | { status: 'loading' }
    | { status: 'ready'; ids: string[]; persistentHistory: boolean }
    | { status: 'failed'; message: string }
  >({ status: 'loading' });
  const [driverAttempt, setDriverAttempt] = useState(0);
  const availableDrivers = driverState.status === 'ready'
    ? supportedDrivers.filter(id => driverState.ids.includes(id)) : [];
  const [submission, setSubmission] = useState<SubmissionState>({ status: 'idle' });
  const activeRequest = useRef<AbortController | null>(null);
  const [runIds, setRunIds] = useState<string[]>([]);
  const [historyVersion, setHistoryVersion] = useState(0);
  const refreshHistory = () => setHistoryVersion(version => version + 1);
  // Only runs started here refresh history when they finish; opening a past
  // run from history must not trigger a reload.
  const submittedRunIds = useRef(new Set<string>());

  useEffect(() => {
    if (getBenchmarkDriversForProvider(provider).length === 0) return;
    const controller = new AbortController();
    setDriverState({ status: 'loading' });
    getBenchmarkServiceStatus(pluginFetch, { signal: controller.signal }).then(
      ({ drivers, persistentHistory }) => {
        if (!controller.signal.aborted) setDriverState({ status: 'ready', ids: drivers, persistentHistory });
      },
      error => {
        if (!controller.signal.aborted) setDriverState({ status: 'failed', message: error.message });
      },
    );
    return () => controller.abort();
  }, [pluginFetch, provider, driverAttempt]);

  const addRun = (id: string) => {
    if (runIds.includes(id)) return false;
    setRunIds(ids => (ids.includes(id) ? ids : [id, ...ids]));
    return true;
  };

  useEffect(() => () => {
    activeRequest.current?.abort();
    activeRequest.current = null;
  }, []);

  const submitRun = async (values: BenchmarkFormValues) => {
    if (activeRequest.current || !availableDrivers.includes(values.driver)) return;
    const request = toCreateBenchmarkRunRequest(values, target, provider);
    if (!request) return;

    const controller = new AbortController();
    activeRequest.current = controller;
    setSubmission({ status: 'submitting' });
    try {
      const run = await createBenchmarkRun(pluginFetch, request, { signal: controller.signal });
      if (activeRequest.current !== controller || controller.signal.aborted) return;
      submittedRunIds.current.add(run.id);
      addRun(run.id);
      setSubmission({ status: 'accepted', id: run.id });
      refreshHistory();
    } catch (error) {
      if (activeRequest.current !== controller || controller.signal.aborted) return;
      setSubmission({
        status: 'failed',
        message:
          error instanceof BenchmarkApiError
            ? error.message
            : 'Could not confirm whether the benchmark started. Check the instance before submitting again.',
      });
    } finally {
      if (activeRequest.current === controller) activeRequest.current = null;
    }
  };

  return (
    <Stack spacing={3} sx={{ p: 3, maxWidth: 1100 }}>
      <PageHeader subtitle="Configure and run a benchmark for this database instance." />
      {supportedDrivers.length > 0 && driverState.status === 'failed' ? (
        <Alert severity="error" action={
          <Button color="inherit" onClick={() => setDriverAttempt(attempt => attempt + 1)}>Retry driver check</Button>
        }>{driverState.message}</Alert>
      ) : availableDrivers.length > 0 ? (
        <BenchmarkForm target={target} provider={provider} availableDrivers={availableDrivers}
          isSubmitting={submission.status === 'submitting'} onSubmit={submitRun} />
      ) : (
        <Alert severity="info">
          {provider
            ? supportedDrivers.length === 0
              ? 'Benchmarking is not available for this database provider.'
              : driverState.status === 'loading'
                ? 'Checking available benchmark drivers…'
                : 'No benchmark driver is enabled for this database provider.'
            : 'Waiting for database provider information.'}
        </Alert>
      )}
      {submission.status === 'failed' && <Alert severity="error">{submission.message}</Alert>}
      {runIds.length > 0 && (
        <Stack spacing={1.5} component="section" aria-label="Benchmark runs">
          <Typography variant="h6" component="h3">
            Runs
          </Typography>
          <Box
            sx={{
              display: 'grid',
              gridTemplateColumns: 'repeat(auto-fit, minmax(min(100%, 420px), 1fr))',
              gap: 2,
              alignItems: 'start',
            }}
          >
            {runIds.map(id => (
              <TrackedBenchmarkRun
                key={id}
                pluginFetch={pluginFetch}
                id={id}
                onDismiss={() => setRunIds(ids => ids.filter(runId => runId !== id))}
                onSettled={submittedRunIds.current.has(id) ? refreshHistory : undefined}
              />
            ))}
          </Box>
        </Stack>
      )}
      {/* Like the driver check, history waits for a supported provider. */}
      {supportedDrivers.length > 0 && (
        <BenchmarkRunHistory
          pluginFetch={pluginFetch}
          target={target}
          refreshKey={historyVersion}
          persistentHistory={driverState.status === 'ready' ? driverState.persistentHistory : undefined}
          shownRunIds={runIds}
          onOpen={addRun}
        />
      )}
      <RunLookup onLookup={addRun} />
    </Stack>
  );
}

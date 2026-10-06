import { useEffect, useRef, useState } from 'react';
import { Alert, Box, Stack, Typography } from '@mui/material';
import type { ClusterDetailTabProps, PluginApi } from '@openeverest/plugin-sdk';
import { BenchmarkForm } from './BenchmarkForm';
import { PageHeader } from './PageHeader';
import { RunLookup } from './RunLookup';
import { TrackedBenchmarkRun } from './TrackedBenchmarkRun';
import { BenchmarkApiError, createBenchmarkRun } from './benchmarkApi';
import { toCreateBenchmarkRunRequest } from './benchmarkFormValidation';
import type { BenchmarkFormValues } from './benchmarkFormValidation';
import { targetFromClusterDetailProps } from './benchmarkTarget';
import type { BenchmarkTarget } from './benchmarkTarget';

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
  return (
    <BenchmarkWorkflow
      key={JSON.stringify([target.k8sCluster, target.namespace, target.instance])}
      pluginFetch={pluginFetch}
      target={target}
    />
  );
}

interface BenchmarkWorkflowProps {
  pluginFetch: PluginApi['fetch'];
  target: BenchmarkTarget;
}

function BenchmarkWorkflow({ pluginFetch, target }: BenchmarkWorkflowProps) {
  const [submission, setSubmission] = useState<SubmissionState>({ status: 'idle' });
  const activeRequest = useRef<AbortController | null>(null);
  const [runIds, setRunIds] = useState<string[]>([]);

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
    if (activeRequest.current) return;
    const request = toCreateBenchmarkRunRequest(values, target);
    if (!request) return;

    const controller = new AbortController();
    activeRequest.current = controller;
    setSubmission({ status: 'submitting' });
    try {
      const run = await createBenchmarkRun(pluginFetch, request, { signal: controller.signal });
      if (activeRequest.current !== controller || controller.signal.aborted) return;
      addRun(run.id);
      setSubmission({ status: 'accepted', id: run.id });
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
      <PageHeader subtitle="Measure transaction throughput and latency of this PostgreSQL instance with pgbench." />
      <BenchmarkForm target={target} isSubmitting={submission.status === 'submitting'} onSubmit={submitRun} />
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
              />
            ))}
          </Box>
        </Stack>
      )}
      <RunLookup onLookup={addRun} />
    </Stack>
  );
}

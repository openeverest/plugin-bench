import type { CSSProperties } from 'react';
import type { ClusterDetailTabProps, PluginApi, PluginRouteProps } from '@openeverest/plugin-sdk';
import { BenchmarkForm } from './BenchmarkForm';
import { BenchmarkRunResult } from './BenchmarkRunResult';
import type { BenchmarkFormValues } from './benchmarkForm';
import { toCreateBenchmarkRunRequest } from './benchmarkForm';
import { BenchmarkApiError, createBenchmarkRun } from './benchmarkApi';
import type { BenchmarkRun } from './benchmarkApi';
import { startBenchmarkRunPolling } from './benchmarkRunPolling';
import type { PollingFeedback } from './benchmarkRunPolling';
import { targetFromClusterDetailProps } from './benchmarkTarget';
import type { BenchmarkTarget } from './benchmarkTarget';

const styles: Record<string, CSSProperties> = {
  page: { padding: 24, maxWidth: 1100 },
  heading: { margin: 0, fontSize: 24, fontWeight: 600 },
  subtitle: { margin: '4px 0 24px', color: '#666' },
  stack: { display: 'grid', gap: 16 },
  targetContext: { border: '1px solid #d9d9d9', borderRadius: 8, padding: 14, background: '#fff' },
  targetHeading: { margin: '0 0 10px', fontSize: 16, fontWeight: 600 },
  targetGrid: { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))', gap: 14, margin: 0 },
  targetLabel: { margin: 0, color: '#666', fontSize: 12 },
  targetValue: { margin: '4px 0 0', fontSize: 14, overflowWrap: 'anywhere' },
  success: { margin: 0, color: '#176b36' },
  error: { margin: 0, color: '#b42318' },
  notice: { padding: 14, borderRadius: 6, color: '#345', background: '#eef5ff' },
};

type BenchmarkTabProps = ClusterDetailTabProps & {
  react: PluginApi['React'];
  pluginFetch: PluginApi['fetch'];
};

type SubmissionState =
  | { status: 'idle' }
  | { status: 'submitting' }
  | { status: 'accepted'; id: string }
  | { status: 'failed'; message: string };

type BenchmarkPageProps = PluginRouteProps & {
  react: PluginApi['React'];
};

function PageHeader({ react, subtitle }: { react: PluginApi['React']; subtitle: string }) {
  return react.createElement(
    'header',
    null,
    react.createElement('h2', { style: styles.heading }, 'Performance Benchmark'),
    react.createElement('p', { style: styles.subtitle }, subtitle)
  );
}

export function BenchmarkTab(props: BenchmarkTabProps) {
  const target = targetFromClusterDetailProps(props);
  return props.react.createElement(BenchmarkWorkflow, {
    key: JSON.stringify([target.k8sCluster, target.namespace, target.instance]),
    react: props.react,
    pluginFetch: props.pluginFetch,
    target,
  });
}

function BenchmarkWorkflow({ react, pluginFetch, target }: {
  react: PluginApi['React'];
  pluginFetch: PluginApi['fetch'];
  target: BenchmarkTarget;
}) {
  const [submission, setSubmission] = react.useState<SubmissionState>({ status: 'idle' });
  const activeRequest = react.useRef<AbortController | null>(null);
  const [trackedId, setTrackedId] = react.useState<string | null>(null);
  const [snapshot, setSnapshot] = react.useState<BenchmarkRun | null>(null);
  const [pollingError, setPollingError] = react.useState<PollingFeedback | null>(null);
  const [pollingAttempt, setPollingAttempt] = react.useState(0);
  const stopPolling = react.useRef<(() => void) | null>(null);

  react.useEffect(() => {
    if (!trackedId) return;
    const stop = startBenchmarkRunPolling(pluginFetch, trackedId, {
      onRun: run => {
        setSnapshot(run);
        setPollingError(null);
      },
      onError: setPollingError,
    });
    stopPolling.current = stop;
    return () => {
      stop();
      if (stopPolling.current === stop) stopPolling.current = null;
    };
  }, [trackedId, pollingAttempt, pluginFetch]);

  react.useEffect(() => () => {
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
      // Stop the previous session immediately, before the new effect starts.
      stopPolling.current?.();
      setTrackedId(run.id);
      setSnapshot(null);
      setPollingError(null);
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

  return react.createElement(
    'div',
    { style: styles.page },
    react.createElement(PageHeader, {
      react,
      subtitle: 'Configure a benchmark for the selected PostgreSQL instance.',
    }),
    react.createElement(
      'div',
      { style: styles.stack },
      react.createElement(
        'section',
        { style: styles.targetContext, 'aria-label': 'Selected database target' },
        react.createElement('h3', { style: styles.targetHeading }, 'Database target'),
        react.createElement(
          'dl',
          { style: styles.targetGrid },
          react.createElement(
            'div',
            null,
            react.createElement('dt', { style: styles.targetLabel }, 'Kubernetes cluster'),
            react.createElement('dd', { style: styles.targetValue }, target.k8sCluster)
          ),
          react.createElement(
            'div',
            null,
            react.createElement('dt', { style: styles.targetLabel }, 'Instance'),
            react.createElement('dd', { style: styles.targetValue }, target.instance)
          ),
          react.createElement(
            'div',
            null,
            react.createElement('dt', { style: styles.targetLabel }, 'Namespace'),
            react.createElement('dd', { style: styles.targetValue }, target.namespace)
          )
        )
      ),
      react.createElement(BenchmarkForm, {
        react,
        isSubmitting: submission.status === 'submitting',
        onSubmit: submitRun,
      }),
      trackedId && react.createElement(BenchmarkRunResult, {
        react, id: trackedId, run: snapshot, feedback: pollingError,
        onRetry: () => {
          setPollingError(null);
          setPollingAttempt(attempt => attempt + 1);
        },
      }),
      submission.status === 'failed' &&
        react.createElement('p', { style: styles.error, role: 'alert' }, submission.message)
    )
  );
}

export function BenchmarkPage({ react }: BenchmarkPageProps) {
  return react.createElement(
    'div',
    { style: styles.page },
    react.createElement(PageHeader, {
      react,
      subtitle: 'Open a PostgreSQL cluster to configure and run a benchmark.',
    }),
    react.createElement(
      'div',
      { style: styles.notice },
      'The standalone benchmark workflow is planned after the MVP.'
    )
  );
}

import type { CSSProperties, FormEvent } from 'react';
import type { ClusterDetailTabProps, PluginApi, PluginRouteProps } from '@openeverest/plugin-sdk';
import { BenchmarkForm } from './BenchmarkForm';
import { BenchmarkRunResult } from './BenchmarkRunResult';
import type { BenchmarkFormValues } from './benchmarkFormValidation';
import { toCreateBenchmarkRunRequest } from './benchmarkFormValidation';
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
  results: { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(100%, 420px), 1fr))', gap: 16, alignItems: 'start' },
  lookup: { display: 'grid', gap: 10, padding: 16, border: '1px solid #d9d9d9', borderRadius: 8, background: '#fff' },
  lookupRow: { display: 'flex', flexWrap: 'wrap', gap: 10 },
  lookupInput: { flex: '1 1 280px', minWidth: 0, padding: '10px 12px', border: '1px solid #aaa', borderRadius: 4, font: 'inherit' },
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
  const [runIds, setRunIds] = react.useState<string[]>([]);

  const addRun = (id: string) => {
    if (runIds.includes(id)) return false;
    setRunIds(ids => ids.includes(id) ? ids : [...ids, id]);
    return true;
  };

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
      react.createElement(RunLookup, { react, onLookup: addRun }),
      runIds.length > 0 && react.createElement('div', { style: styles.results },
        ...runIds.map(id => react.createElement(TrackedBenchmarkRun, {
          key: id, react, pluginFetch, id,
          onDismiss: () => setRunIds(ids => ids.filter(runId => runId !== id)),
        }))),
      submission.status === 'failed' &&
        react.createElement('p', { style: styles.error, role: 'alert' }, submission.message)
    )
  );
}

function RunLookup({ react, onLookup }: {
  react: PluginApi['React'];
  onLookup: (id: string) => boolean;
}) {
  const [id, setId] = react.useState('');
  const [message, setMessage] = react.useState('');
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const runId = id.trim();
    if (!runId) {
      setMessage('Enter a run ID.');
      return;
    }
    if (!onLookup(runId)) {
      setMessage('This run is already shown below.');
      return;
    }
    setMessage('');
  };

  return react.createElement('form', { style: styles.lookup, onSubmit: submit },
    react.createElement('h3', { style: styles.targetHeading }, 'Look up a benchmark run'),
    react.createElement('div', { style: styles.lookupRow },
      react.createElement('input', {
        style: styles.lookupInput,
        type: 'text',
        value: id,
        onChange: (event: { currentTarget: { value: string } }) => setId(event.currentTarget.value),
        placeholder: 'Enter run ID',
        'aria-label': 'Benchmark run ID',
        'aria-describedby': message ? 'run-lookup-message' : undefined,
      }),
      react.createElement('button', { type: 'submit' }, 'Find run')),
    message && react.createElement('p', { id: 'run-lookup-message', style: styles.error, role: 'alert' }, message));
}

function TrackedBenchmarkRun({ react, pluginFetch, id, onDismiss }: {
  react: PluginApi['React'];
  pluginFetch: PluginApi['fetch'];
  id: string;
  onDismiss: () => void;
}) {
  const [run, setRun] = react.useState<BenchmarkRun | null>(null);
  const [feedback, setFeedback] = react.useState<PollingFeedback | null>(null);
  const [attempt, setAttempt] = react.useState(0);

  react.useEffect(() => {
    if (run && run.status !== 'running') return;
    return startBenchmarkRunPolling(pluginFetch, id, {
      onRun: snapshot => { setRun(snapshot); setFeedback(null); },
      onError: setFeedback,
    });
    // Snapshots update this card without restarting its polling session.
  }, [pluginFetch, id, attempt]);

  return react.createElement(BenchmarkRunResult, {
    react, id, run, feedback,
    onRetry: () => { setFeedback(null); setAttempt(value => value + 1); },
    onDismiss: run && run.status !== 'running' ? onDismiss : undefined,
  });
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

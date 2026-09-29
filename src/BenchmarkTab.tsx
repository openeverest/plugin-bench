import type { CSSProperties } from 'react';
import type { ClusterDetailTabProps, PluginApi, PluginRouteProps } from '@openeverest/plugin-sdk';
import { BenchmarkForm } from './BenchmarkForm';

type RunSummary = {
  id: string;
  status: string;
  profile: string;
  createdAt: string;
};

const styles: Record<string, CSSProperties> = {
  page: { padding: 24, maxWidth: 1100 },
  heading: { margin: 0, fontSize: 24, fontWeight: 600 },
  subtitle: { margin: '4px 0 24px', color: '#666' },
  stack: { display: 'grid', gap: 16 },
  targetContext: { border: '1px solid #d9d9d9', borderRadius: 8, padding: 16, background: '#fff' },
  targetHeading: { margin: '0 0 12px', fontSize: 16, fontWeight: 600 },
  targetGrid: { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))', gap: 12, margin: 0 },
  targetLabel: { margin: 0, color: '#666', fontSize: 12 },
  targetValue: { margin: '4px 0 0', fontSize: 14, overflowWrap: 'anywhere' },
  card: { border: '1px solid #d9d9d9', borderRadius: 8, padding: 20, background: '#fff' },
  cardHeading: { margin: '0 0 16px', fontSize: 18, fontWeight: 600 },
  caption: { margin: 0, color: '#666', fontSize: 12 },
  notice: { padding: 14, borderRadius: 6, color: '#345', background: '#eef5ff' },
  warning: { marginBottom: 16, padding: 14, borderRadius: 6, color: '#7a4b00', background: '#fff4d6' },
  table: { width: '100%', borderCollapse: 'collapse', fontSize: 14 },
  tableCell: { padding: '10px 8px', borderBottom: '1px solid #ddd', textAlign: 'left' },
};

type BenchmarkTabProps = ClusterDetailTabProps & {
  react: PluginApi['React'];
  fetch: PluginApi['fetch'];
};

type BenchmarkPageProps = PluginRouteProps & {
  react: PluginApi['React'];
};

async function apiFetch(fetcher: PluginApi['fetch'], path: string): Promise<unknown> {
  const response = await fetcher(`/api${path}`);
  if (!response.ok) {
    const message = await response.text().catch(() => '');
    throw new Error(message || `Request failed with HTTP ${response.status}`);
  }
  return response.json();
}

function PageHeader({ react, subtitle }: { react: PluginApi['React']; subtitle: string }) {
  return react.createElement(
    'header',
    null,
    react.createElement('h2', { style: styles.heading }, 'Performance Benchmark'),
    react.createElement('p', { style: styles.subtitle }, subtitle)
  );
}

function RunHistory({
  react,
  runs,
  loading,
}: {
  react: PluginApi['React'];
  runs: RunSummary[];
  loading: boolean;
}) {
  return react.createElement(
    'section',
    { style: styles.card },
    react.createElement('h3', { style: styles.cardHeading }, 'Run history'),
    loading
      ? react.createElement('p', { style: styles.caption }, 'Loading runs…')
      : runs.length === 0
        ? react.createElement('p', { style: styles.caption }, 'No benchmark runs yet.')
        : react.createElement(
            'table',
            { style: styles.table },
            react.createElement(
              'thead',
              null,
              react.createElement(
                'tr',
                null,
                ...['Run', 'Profile', 'Status', 'Created'].map((heading) =>
                  react.createElement('th', { key: heading, style: styles.tableCell }, heading)
                )
              )
            ),
            react.createElement(
              'tbody',
              null,
              ...runs.map((run) =>
                react.createElement(
                  'tr',
                  { key: run.id },
                  react.createElement('td', { style: styles.tableCell }, run.id),
                  react.createElement('td', { style: styles.tableCell }, run.profile),
                  react.createElement('td', { style: styles.tableCell }, run.status),
                  react.createElement('td', { style: styles.tableCell }, new Date(run.createdAt).toLocaleString())
                )
              )
            )
          )
  );
}

export function BenchmarkTab(props: BenchmarkTabProps) {
  const { react } = props;
  const [runs, setRuns] = react.useState<RunSummary[]>([]);
  const [loading, setLoading] = react.useState(true);
  const [error, setError] = react.useState<string | null>(null);

  react.useEffect(() => {
    let active = true;
    setLoading(true);
    setError(null);

    apiFetch(props.fetch, '/runs')
      .then((data) => {
        if (active) setRuns((data as { runs?: RunSummary[] }).runs ?? []);
      })
      .catch((reason: unknown) => {
        if (active) setError(reason instanceof Error ? reason.message : String(reason));
      })
      .finally(() => {
        if (active) setLoading(false);
      });

    return () => {
      active = false;
    };
  }, [props.fetch, props.instanceName, props.namespace]);

  return react.createElement(
    'div',
    { style: styles.page },
    react.createElement(PageHeader, {
      react,
      subtitle: 'Configure a benchmark for the selected PostgreSQL instance.',
    }),
    error && react.createElement('div', { style: styles.warning }, error),
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
            react.createElement('dt', { style: styles.targetLabel }, 'Instance'),
            react.createElement('dd', { style: styles.targetValue }, props.instanceName)
          ),
          react.createElement(
            'div',
            null,
            react.createElement('dt', { style: styles.targetLabel }, 'Namespace'),
            react.createElement('dd', { style: styles.targetValue }, props.namespace)
          )
        )
      ),
      react.createElement(BenchmarkForm, { react }),
      react.createElement(RunHistory, { react, runs, loading })
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

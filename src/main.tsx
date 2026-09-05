import type {
  ClusterDetailTabProps,
  PluginApi,
  PluginRegisterFn,
  PluginRouteProps,
} from '@openeverest/plugin-sdk';
import type { CSSProperties } from 'react';

// React is supplied by OpenEverest at runtime, as required by the plugin SDK.
let React: PluginApi['React'];
let pluginFetch: PluginApi['fetch'];

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
  card: { border: '1px solid #d9d9d9', borderRadius: 8, padding: 20, background: '#fff' },
  cardHeading: { margin: '0 0 16px', fontSize: 18, fontWeight: 600 },
  fieldStack: { display: 'grid', gap: 14 },
  label: { display: 'grid', gap: 6, fontSize: 14, color: '#444' },
  control: { padding: '9px 10px', border: '1px solid #bbb', borderRadius: 4, background: '#f5f5f5' },
  actionRow: { display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' },
  button: { padding: '9px 16px', border: 0, borderRadius: 4, color: '#777', background: '#ddd' },
  badge: { padding: '3px 9px', border: '1px solid #bbb', borderRadius: 12, fontSize: 12, color: '#555' },
  caption: { margin: 0, color: '#666', fontSize: 12 },
  notice: { padding: 14, borderRadius: 6, color: '#345', background: '#eef5ff' },
  warning: { marginBottom: 16, padding: 14, borderRadius: 6, color: '#7a4b00', background: '#fff4d6' },
  table: { width: '100%', borderCollapse: 'collapse', fontSize: 14 },
  tableCell: { padding: '10px 8px', borderBottom: '1px solid #ddd', textAlign: 'left' },
};

async function apiFetch(path: string, options?: RequestInit): Promise<unknown> {
  const response = await pluginFetch(`/api${path}`, options);
  if (!response.ok) {
    const message = await response.text().catch(() => '');
    throw new Error(message || `Request failed with HTTP ${response.status}`);
  }
  return response.json();
}

const PageHeader = ({ subtitle }: { subtitle: string }) =>
  React.createElement(
    'header',
    null,
    React.createElement('h2', { style: styles.heading }, 'Performance Benchmark'),
    React.createElement('p', { style: styles.subtitle }, subtitle)
  );

const RunConfiguration = () =>
  React.createElement(
    'section',
    { style: styles.card },
    React.createElement('h3', { style: styles.cardHeading }, 'Run configuration'),
    React.createElement(
      'div',
      { style: styles.fieldStack },
      React.createElement(
        'label',
        { style: styles.label },
        'Profile',
        React.createElement(
          'select',
          { style: styles.control, value: 'smoke', disabled: true, onChange: () => undefined },
          React.createElement('option', { value: 'smoke' }, 'Smoke test')
        )
      ),
      React.createElement(
        'label',
        { style: styles.label },
        'Duration',
        React.createElement('input', { style: styles.control, value: '30 seconds', disabled: true, readOnly: true })
      ),
      React.createElement(
        'div',
        { style: styles.actionRow },
        React.createElement('button', { type: 'button', style: styles.button, disabled: true }, 'Start benchmark'),
        React.createElement('span', { style: styles.badge }, 'Runner not implemented yet')
      ),
      React.createElement(
        'p',
        { style: styles.caption },
        'The scaffold reserves this workflow for the PostgreSQL benchmark runner.'
      )
    )
  );

const RunHistory = ({ runs, loading }: { runs: RunSummary[]; loading: boolean }) =>
  React.createElement(
    'section',
    { style: styles.card },
    React.createElement('h3', { style: styles.cardHeading }, 'Run history'),
    loading
      ? React.createElement('p', { style: styles.caption }, 'Loading runs…')
      : runs.length === 0
        ? React.createElement('p', { style: styles.caption }, 'No benchmark runs yet.')
        : React.createElement(
            'table',
            { style: styles.table },
            React.createElement(
              'thead',
              null,
              React.createElement(
                'tr',
                null,
                ...['Run', 'Profile', 'Status', 'Created'].map((heading) =>
                  React.createElement('th', { key: heading, style: styles.tableCell }, heading)
                )
              )
            ),
            React.createElement(
              'tbody',
              null,
              ...runs.map((run) =>
                React.createElement(
                  'tr',
                  { key: run.id },
                  React.createElement('td', { style: styles.tableCell }, run.id),
                  React.createElement('td', { style: styles.tableCell }, run.profile),
                  React.createElement('td', { style: styles.tableCell }, run.status),
                  React.createElement('td', { style: styles.tableCell }, new Date(run.createdAt).toLocaleString())
                )
              )
            )
          )
  );

const BenchmarkTab = (props: ClusterDetailTabProps) => {
  const [runs, setRuns] = React.useState<RunSummary[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState<string | null>(null);

  React.useEffect(() => {
    let active = true;
    setLoading(true);
    setError(null);

    apiFetch('/runs')
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
  }, [props.instanceName, props.namespace]);

  const cluster = props.cluster as { metadata?: { name?: string }; name?: string };
  const clusterName = cluster?.metadata?.name ?? cluster?.name ?? props.instanceName;

  return React.createElement(
    'div',
    { style: styles.page },
    React.createElement(PageHeader, {
      subtitle: `PostgreSQL benchmark workspace for ${clusterName} in ${props.namespace}`,
    }),
    error && React.createElement('div', { style: styles.warning }, error),
    React.createElement(
      'div',
      { style: styles.stack },
      React.createElement(RunConfiguration),
      React.createElement(RunHistory, { runs, loading })
    )
  );
};

const BenchmarkPage = (_props: PluginRouteProps) =>
  React.createElement(
    'div',
    { style: styles.page },
    React.createElement(PageHeader, {
      subtitle: 'Open a PostgreSQL cluster to configure and run a benchmark.',
    }),
    React.createElement(
      'div',
      { style: styles.notice },
      'The standalone benchmark workflow is planned after the MVP.'
    )
  );

const register: PluginRegisterFn = (api: PluginApi) => {
  React = api.React;
  pluginFetch = api.fetch.bind(api);

  api.registerExtension({
    type: 'route',
    label: 'Performance Benchmark',
    component: BenchmarkPage,
  });

  api.registerExtension({
    type: 'clusterDetailTab',
    label: 'Performance Benchmark',
    path: 'performance-benchmark',
    providers: ['provider-cloudnative-pg', 'provider-percona-postgresql'],
    component: BenchmarkTab,
  });
};

export default register;

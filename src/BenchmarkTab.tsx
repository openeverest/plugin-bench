import type { CSSProperties } from 'react';
import type { ClusterDetailTabProps, PluginApi, PluginRouteProps } from '@openeverest/plugin-sdk';
import { BenchmarkForm } from './BenchmarkForm';
import { targetFromClusterDetailProps } from './benchmarkTarget';

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
  notice: { padding: 14, borderRadius: 6, color: '#345', background: '#eef5ff' },
};

type BenchmarkTabProps = ClusterDetailTabProps & {
  react: PluginApi['React'];
};

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
  const { react } = props;
  const target = targetFromClusterDetailProps(props);

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
      react.createElement(BenchmarkForm, { react })
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

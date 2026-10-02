import type { CSSProperties } from 'react';
import type { PluginApi } from '@openeverest/plugin-sdk';
import type { BenchmarkRun } from './benchmarkApi';
import type { PollingFeedback } from './benchmarkRunPolling';

type Props = {
  react: PluginApi['React'];
  id: string;
  run: BenchmarkRun | null;
  feedback: PollingFeedback | null; 
  onRetry: () => void;
};

const styles: Record<string, CSSProperties> = {
  panel: { border: '1px solid #d9d9d9', borderRadius: 8, padding: 16, background: '#fff', minWidth: 0 },
  heading: { margin: '0 0 12px', fontSize: 16 },
  status: { margin: '0 0 12px', fontWeight: 600 },
  details: { display: 'grid', gap: 8, margin: 0, overflowWrap: 'anywhere' },
  label: { color: '#666', fontSize: 12 },
  value: { margin: '4px 0 0', fontSize: 14 },
  error: { color: '#b42318', overflowWrap: 'anywhere' },
  notice: { color: '#666' },
  output: { maxHeight: 400, overflow: 'auto', padding: 12, background: '#f5f6f8',
    borderRadius: 6, whiteSpace: 'pre', fontSize: 13 },
};

export function BenchmarkRunResult({ react, id, run, feedback, onRetry }: Props) {
  const element = react.createElement;
  const labels = { running: 'Running', succeeded: 'Succeeded', failed: 'Failed' };
  const colors = { running: '#1964b8', succeeded: '#176b36', failed: '#b42318' };
  const detail = (label: string, value: string) => element('div', { key: label },
    element('dt', { style: styles.label }, label), element('dd', { style: styles.value }, value));
  const timestamp = (value: string) => new Date(value).toLocaleString();
  const completed = run !== null && run.status !== 'running';

  return element('section', { style: styles.panel, 'aria-label': 'Benchmark run result' },
    element('h3', { style: styles.heading }, 'Benchmark run'),
    element('p', { role: 'status', style: { ...styles.status, color: run ? colors[run.status] : '#666' } },
      run ? `${feedback ? 'Last known status: ' : 'Status: '}${labels[run.status]}`
        : feedback ? 'Run accepted. Status unavailable.' : 'Run accepted. Checking status…'),
    element('dl', { style: styles.details },
      detail('Run ID', id),
      run && detail('Created', timestamp(run.createdAt)),
      run?.completedAt && detail('Completed', timestamp(run.completedAt))),
    feedback && element('div', null,
      element('p', { role: 'alert', style: styles.error },
        `Status updates ${feedback.paused ? 'paused' : 'interrupted'}. ${feedback.error.message}`,
        feedback.retryInMs !== undefined ? ` Retrying in ${feedback.retryInMs / 1000} seconds.` : ''),
      feedback.paused && element('button', { type: 'button', onClick: onRetry }, 'Retry status check')),
    run?.status === 'failed' && element('p', { role: 'alert', style: styles.error }, run.error),
    completed && element('div', null,
      element('h4', null, 'Benchmark output'),
      run.outputTruncated && element('p', { style: styles.notice }, 'Output was truncated by the backend.'),
      run.output ? element('pre', { style: styles.output, tabIndex: 0, 'aria-label': 'Benchmark output' }, run.output)
        : element('p', { style: styles.notice }, 'No output was captured.')));
}

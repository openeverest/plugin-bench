import type { BenchmarkRunStatus } from './benchmarkApi';

export const STATUS_LABELS: Record<BenchmarkRunStatus, string> = {
  running: 'Running',
  succeeded: 'Succeeded',
  failed: 'Failed',
};

export const STATUS_COLORS: Record<BenchmarkRunStatus, 'info' | 'success' | 'error'> = {
  running: 'info',
  succeeded: 'success',
  failed: 'error',
};

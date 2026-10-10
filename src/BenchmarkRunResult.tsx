import { Fragment } from 'react';
import {
  Alert,
  Box,
  Button,
  Card,
  CardActions,
  CardContent,
  Chip,
  CircularProgress,
  Stack,
  Typography,
} from '@mui/material';
import type { BenchmarkRun, BenchmarkRunStatus } from './benchmarkApi';
import type { PollingFeedback } from './benchmarkRunPolling';
import { BenchmarkNodeAffinitySummary } from './BenchmarkNodeAffinitySummary.js';

const STATUS_LABELS: Record<BenchmarkRunStatus, string> = {
  running: 'Running',
  succeeded: 'Succeeded',
  failed: 'Failed',
};

const STATUS_COLORS: Record<BenchmarkRunStatus, 'info' | 'success' | 'error'> = {
  running: 'info',
  succeeded: 'success',
  failed: 'error',
};

interface BenchmarkRunResultProps {
  id: string;
  run: BenchmarkRun | null;
  feedback: PollingFeedback | null;
  onRetry: () => void;
  onDismiss?: () => void;
}

function statusLabel(run: BenchmarkRun | null, feedback: PollingFeedback | null): string {
  if (run) return `${feedback ? 'Last known status' : 'Status'}: ${STATUS_LABELS[run.status]}`;
  return feedback ? 'Status unavailable' : 'Checking status…';
}

const formatTimestamp = (value: string) => new Date(value).toLocaleString();

export function BenchmarkRunResult({ id, run, feedback, onRetry, onDismiss }: BenchmarkRunResultProps) {
  const completed = run !== null && run.status !== 'running';
  const pollingActive = !feedback && (run === null || run.status === 'running');
  const details: Array<[string, string | undefined]> = [
    ['Run ID', id],
    ['Database', run?.database],
    ['Created', run ? formatTimestamp(run.createdAt) : undefined],
    ['Completed', run?.completedAt ? formatTimestamp(run.completedAt) : undefined],
  ];

  return (
    <Card variant="outlined" component="section" aria-label="Benchmark run result" sx={{ minWidth: 0 }}>
      <CardContent>
        <Stack spacing={2}>
          <Stack direction="row" spacing={1} sx={{ justifyContent: 'space-between', alignItems: 'center' }}>
            <Typography variant="h6" component="h3">
              Benchmark run
            </Typography>
            <Chip
              role="status"
              size="small"
              label={statusLabel(run, feedback)}
              color={run ? STATUS_COLORS[run.status] : 'default'}
              icon={pollingActive ? <CircularProgress size={12} color="inherit" /> : undefined}
            />
          </Stack>

          <Box
            component="dl"
            sx={{ display: 'grid', gridTemplateColumns: 'auto 1fr', columnGap: 2, rowGap: 0.5, m: 0 }}
          >
            {details.map(([label, value]) =>
              value === undefined ? null : (
                <Fragment key={label}>
                  <Typography component="dt" variant="body2" color="text.secondary">
                    {label}
                  </Typography>
                  <Typography component="dd" variant="body2" sx={{ m: 0, overflowWrap: 'anywhere' }}>
                    {value}
                  </Typography>
                </Fragment>
              )
            )}
          </Box>

          {feedback && (
            <Alert
              severity={feedback.paused ? 'error' : 'warning'}
              action={
                feedback.paused ? (
                  <Button color="inherit" size="small" onClick={onRetry}>
                    Retry status check
                  </Button>
                ) : undefined
              }
            >
              {`Status updates ${feedback.paused ? 'paused' : 'interrupted'}. ${feedback.error.message}`}
              {feedback.retryInMs !== undefined ? ` Retrying in ${feedback.retryInMs / 1000} seconds.` : ''}
            </Alert>
          )}

          {run?.status === 'failed' && <Alert severity="error">{run.error}</Alert>}

          {run?.nodeAffinity !== undefined && <BenchmarkNodeAffinitySummary affinity={run.nodeAffinity} />}

          {completed && (
            <Box>
              <Typography variant="subtitle2" component="h4" gutterBottom>
                Benchmark output
              </Typography>
              {run.outputTruncated && (
                <Typography variant="caption" component="p" color="text.secondary" gutterBottom>
                  Output was truncated by the backend.
                </Typography>
              )}
              {run.output ? (
                <Box
                  component="pre"
                  tabIndex={0}
                  aria-label="Benchmark output"
                  sx={{
                    m: 0,
                    p: 1.5,
                    maxHeight: 400,
                    overflow: 'auto',
                    bgcolor: 'action.hover',
                    borderRadius: 1,
                    fontFamily: 'monospace',
                    fontSize: 13,
                    whiteSpace: 'pre',
                  }}
                >
                  {run.output}
                </Box>
              ) : (
                <Typography variant="body2" color="text.secondary">
                  No output was captured.
                </Typography>
              )}
            </Box>
          )}
        </Stack>
      </CardContent>
      {completed && onDismiss && (
        <CardActions sx={{ px: 2, pb: 2 }}>
          <Button size="small" onClick={onDismiss} aria-label={`Dismiss completed run ${id}`}>
            Dismiss
          </Button>
        </CardActions>
      )}
    </Card>
  );
}

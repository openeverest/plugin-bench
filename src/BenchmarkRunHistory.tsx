import { useEffect, useState } from 'react';
import {
  Alert, Button, Chip, CircularProgress, Stack, Table, TableBody, TableCell, TableContainer, TableHead,
  TableRow, Typography,
} from '@mui/material';
import type { PluginApi } from '@openeverest/plugin-sdk';
import { listBenchmarkRuns } from './benchmarkHistoryApi';
import type { BenchmarkRunSummary } from './benchmarkHistoryApi';
import { formatMetricValue } from './benchmarkMetricFormat';
import { STATUS_COLORS, STATUS_LABELS } from './benchmarkRunStatus';
import type { BenchmarkTarget } from './benchmarkTarget';
import { benchmarkDrivers, isBenchmarkDriverId } from './drivers';

interface BenchmarkRunHistoryProps {
  pluginFetch: PluginApi['fetch'];
  target: BenchmarkTarget;
  /** Changing this value reloads the history, e.g. after a run finishes. */
  refreshKey: number;
  /** Undefined until the service status is known. */
  persistentHistory: boolean | undefined;
  shownRunIds: readonly string[];
  onOpen: (id: string) => void;
}

type HistoryState =
  | { status: 'loading'; runs?: BenchmarkRunSummary[] }
  | { status: 'ready'; runs: BenchmarkRunSummary[] }
  | { status: 'failed'; message: string; runs?: BenchmarkRunSummary[] };

const driverLabel = (driver: string | undefined) =>
  driver === undefined ? '—' : isBenchmarkDriverId(driver) ? benchmarkDrivers[driver].label : driver;

function formatRunDuration(run: Pick<BenchmarkRunSummary, 'createdAt' | 'completedAt'>): string {
  if (!run.completedAt) return '—';
  const seconds = Math.max(0, Math.round((Date.parse(run.completedAt) - Date.parse(run.createdAt)) / 1000));
  if (seconds < 60) return `${seconds} s`;
  const minutes = Math.floor(seconds / 60);
  return seconds % 60 === 0 ? `${minutes} min` : `${minutes} min ${seconds % 60} s`;
}

export function BenchmarkRunHistory({
  pluginFetch, target, refreshKey, persistentHistory, shownRunIds, onOpen,
}: BenchmarkRunHistoryProps) {
  const [state, setState] = useState<HistoryState>({ status: 'loading' });
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    // Keep the previous rows visible while refreshing to avoid flicker.
    setState(previous => ({ status: 'loading', runs: previous.runs }));
    listBenchmarkRuns(pluginFetch, target, { signal: controller.signal }).then(
      runs => { if (!controller.signal.aborted) setState({ status: 'ready', runs }); },
      error => {
        if (controller.signal.aborted) return;
        setState(previous => ({ status: 'failed', message: error.message, runs: previous.runs }));
      },
    );
    return () => controller.abort();
  }, [pluginFetch, target.k8sCluster, target.namespace, target.instance, refreshKey, attempt]);

  const runs = state.runs;
  return (
    <Stack spacing={1.5} component="section" aria-label="Benchmark history">
      <Stack direction="row" spacing={1} sx={{ alignItems: 'center', justifyContent: 'space-between' }}>
        <Typography variant="h6" component="h3">History</Typography>
        <Stack direction="row" spacing={1} sx={{ alignItems: 'center' }}>
          {state.status === 'loading' && <CircularProgress size={18} aria-label="Loading benchmark history" />}
          <Button size="small" disabled={state.status === 'loading'} onClick={() => setAttempt(value => value + 1)}>
            Refresh
          </Button>
        </Stack>
      </Stack>
      {persistentHistory === false && (
        <Typography variant="body2" color="text.secondary">
          History is kept in the benchmark service's memory: it is lost when the service restarts, and completed
          runs are removed after one hour.
        </Typography>
      )}
      {state.status === 'failed' && (
        <Alert severity="error" action={
          <Button color="inherit" onClick={() => setAttempt(value => value + 1)}>Retry</Button>
        }>{state.message}</Alert>
      )}
      {runs && runs.length === 0 && state.status === 'ready' && (
        <Typography variant="body2" color="text.secondary">
          No benchmark runs have been recorded for this instance yet.
        </Typography>
      )}
      {runs && runs.length > 0 && (
        <TableContainer>
          <Table size="small" aria-label="Benchmark history">
            <TableHead>
              <TableRow>
                <TableCell>Started</TableCell>
                <TableCell>Tool</TableCell>
                <TableCell>Status</TableCell>
                <TableCell>Duration</TableCell>
                <TableCell>Results</TableCell>
                <TableCell align="right">Details</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {runs.map(run => {
                const shown = shownRunIds.includes(run.id);
                return (
                  <TableRow key={run.id}>
                    <TableCell component="th" scope="row" sx={{ whiteSpace: 'nowrap' }}>
                      {new Date(run.createdAt).toLocaleString()}
                    </TableCell>
                    <TableCell>{driverLabel(run.driver)}</TableCell>
                    <TableCell>
                      <Chip size="small" label={STATUS_LABELS[run.status]} color={STATUS_COLORS[run.status]} />
                    </TableCell>
                    <TableCell sx={{ whiteSpace: 'nowrap' }}>{formatRunDuration(run)}</TableCell>
                    <TableCell sx={{ minWidth: 160 }}>
                      {run.metrics && run.metrics.length > 0 ? (
                        <Stack>
                          {run.metrics.map(metric => (
                            <Typography key={JSON.stringify([metric.name, metric.unit])} variant="body2" component="span"
                              sx={{ whiteSpace: 'nowrap' }}>
                              {`${metric.name} ${formatMetricValue(metric.value)} ${metric.unit}`}
                            </Typography>
                          ))}
                        </Stack>
                      ) : run.status === 'failed' ? run.error : '—'}
                    </TableCell>
                    <TableCell align="right">
                      <Button size="small" disabled={shown} onClick={() => onOpen(run.id)}
                        aria-label={shown ? `Run ${run.id} is shown` : `View run ${run.id}`}>
                        {shown ? 'Shown' : 'View'}
                      </Button>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </TableContainer>
      )}
    </Stack>
  );
}

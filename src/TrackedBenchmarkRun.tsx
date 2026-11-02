import { useEffect, useRef, useState } from 'react';
import type { PluginApi } from '@openeverest/plugin-sdk';
import { BenchmarkRunResult } from './BenchmarkRunResult';
import type { BenchmarkRun } from './benchmarkApi';
import { startBenchmarkRunPolling } from './benchmarkRunPolling';
import type { PollingFeedback } from './benchmarkRunPolling';

interface TrackedBenchmarkRunProps {
  pluginFetch: PluginApi['fetch'];
  id: string;
  onDismiss: () => void;
  /** Called once when the run is observed in a terminal status. */
  onSettled?: () => void;
}

export function TrackedBenchmarkRun({ pluginFetch, id, onDismiss, onSettled }: TrackedBenchmarkRunProps) {
  const [run, setRun] = useState<BenchmarkRun | null>(null);
  const [feedback, setFeedback] = useState<PollingFeedback | null>(null);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    if (run && run.status !== 'running') return;
    return startBenchmarkRunPolling(pluginFetch, id, {
      onRun: snapshot => { setRun(snapshot); setFeedback(null); },
      onError: setFeedback,
    });
    // Snapshots update this card without restarting its polling session.
  }, [pluginFetch, id, attempt]);

  const settled = run !== null && run.status !== 'running';
  const onSettledRef = useRef(onSettled);
  onSettledRef.current = onSettled;
  useEffect(() => {
    if (settled) onSettledRef.current?.();
  }, [settled]);

  return (
    <BenchmarkRunResult
      id={id}
      run={run}
      feedback={feedback}
      onRetry={() => { setFeedback(null); setAttempt(value => value + 1); }}
      onDismiss={run && run.status !== 'running' ? onDismiss : undefined}
    />
  );
}

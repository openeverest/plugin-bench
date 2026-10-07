import { Box, Typography } from '@mui/material';
import type { BenchmarkTarget } from './benchmarkTarget';

interface BenchmarkTargetSummaryProps {
  target: BenchmarkTarget;
}

export function BenchmarkTargetSummary({ target }: BenchmarkTargetSummaryProps) {
  const entries: Array<[string, string]> = [
    ['Instance', target.instance],
    ['Namespace', target.namespace],
    ['Kubernetes cluster', target.k8sCluster],
  ];

  return (
    <Box
      component="dl"
      aria-label="Selected database target"
      sx={{ display: 'flex', flexWrap: 'wrap', columnGap: 4, rowGap: 1, m: 0 }}
    >
      {entries.map(([label, value]) => (
        <Box key={label} sx={{ minWidth: 0 }}>
          <Typography component="dt" variant="caption" color="text.secondary">
            {label}
          </Typography>
          <Typography component="dd" variant="body2" sx={{ m: 0, overflowWrap: 'anywhere' }}>
            {value}
          </Typography>
        </Box>
      ))}
    </Box>
  );
}

import { Alert, Stack } from '@mui/material';
import { PageHeader } from './PageHeader';

export function BenchmarkPage() {
  return (
    <Stack spacing={3} sx={{ p: 3, maxWidth: 1100 }}>
      <PageHeader subtitle="Run PostgreSQL and MySQL benchmarks for your database instances." />
      <Alert severity="info">
        Open a supported PostgreSQL or MySQL instance and select the Performance Benchmark tab to configure and start a run.
      </Alert>
    </Stack>
  );
}

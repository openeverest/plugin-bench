import {
  Box, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, Typography,
} from '@mui/material';
import type { BenchmarkMetric } from './benchmarkApi';

export function BenchmarkRunMetrics({ metrics }: { metrics?: readonly BenchmarkMetric[] }) {
  return (
    <Box component="section" aria-label="Benchmark metrics">
      <Typography variant="subtitle2" component="h4" gutterBottom>Metrics</Typography>
      {metrics && metrics.length > 0 ? (
        <TableContainer>
          <Table size="small" aria-label="Benchmark metrics">
            <TableHead>
              <TableRow>
                <TableCell scope="col">Metric</TableCell>
                <TableCell scope="col" align="right">Value</TableCell>
                <TableCell scope="col">Unit</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {metrics.map(metric => (
                <TableRow key={JSON.stringify([metric.name, metric.unit])}>
                  <TableCell component="th" scope="row" sx={{ overflowWrap: 'anywhere' }}>{metric.name}</TableCell>
                  <TableCell align="right">{metric.value}</TableCell>
                  <TableCell sx={{ overflowWrap: 'anywhere' }}>{metric.unit}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      ) : (
        <Typography variant="body2" color="text.secondary">Structured metrics unavailable.</Typography>
      )}
    </Box>
  );
}

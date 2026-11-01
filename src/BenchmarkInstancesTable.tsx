import { Button, Link, Paper, Table, TableBody, TableCell, TableContainer, TableHead, TableRow } from '@mui/material';
import type { BenchmarkInstance } from './benchmarkInstancesApi';
import { benchmarkInstanceHref, benchmarkInstanceKey } from './benchmarkInstanceNavigation';

export function BenchmarkInstancesTable({ instances, currentCluster }: {
  instances: readonly BenchmarkInstance[];
  currentCluster: string;
}) {
  return (
    <TableContainer component={Paper} variant="outlined">
      <Table aria-label="Available benchmark instances">
        <TableHead>
          <TableRow>
            <TableCell>Name</TableCell>
            <TableCell>Namespace</TableCell>
            <TableCell>Kubernetes cluster</TableCell>
            <TableCell align="right">Action</TableCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {instances.map(instance => {
            const href = benchmarkInstanceHref(instance, currentCluster);
            return (
              <TableRow key={benchmarkInstanceKey(instance)}>
                <TableCell component="th" scope="row"><Link href={href}>{instance.name}</Link></TableCell>
                <TableCell>{instance.namespace}</TableCell>
                <TableCell>{instance.k8sCluster}</TableCell>
                <TableCell align="right">
                  <Button variant="contained" color="primary" href={href}
                    aria-label={`Benchmark ${instance.namespace}/${instance.name}`}>Benchmark</Button>
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </TableContainer>
  );
}

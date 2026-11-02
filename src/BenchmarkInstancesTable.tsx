import { Button, Link, Paper, Table, TableBody, TableCell, TableContainer, TableHead, TableRow } from '@mui/material';
import type { BenchmarkInstance } from './benchmarkInstancesApi';
import { benchmarkInstanceHref, benchmarkInstanceKey, enabledDriversForInstance } from './benchmarkInstanceNavigation';
import { benchmarkDrivers } from './drivers';

export function BenchmarkInstancesTable({ instances, enabledDrivers, currentCluster }: {
  instances: readonly BenchmarkInstance[];
  enabledDrivers: readonly string[];
  currentCluster: string;
}) {
  return (
    <TableContainer component={Paper} variant="outlined">
      <Table aria-label="Available benchmark instances">
        <TableHead>
          <TableRow>
            <TableCell>Name</TableCell>
            <TableCell>Namespace</TableCell>
            <TableCell>Provider</TableCell>
            <TableCell>Benchmark tool</TableCell>
            <TableCell align="right">Action</TableCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {instances.map(instance => {
            const href = benchmarkInstanceHref(instance, currentCluster);
            const tools = enabledDriversForInstance(instance, enabledDrivers)
              .map(driver => benchmarkDrivers[driver].label).join(', ');
            return (
              <TableRow key={benchmarkInstanceKey(instance)}>
                <TableCell component="th" scope="row"><Link href={href}>{instance.name}</Link></TableCell>
                <TableCell>{instance.namespace}</TableCell>
                <TableCell>{instance.provider}</TableCell>
                <TableCell>{tools}</TableCell>
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

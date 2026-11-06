import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { BenchmarkForm } from '../src/BenchmarkForm';
import { toCreateBenchmarkRunRequest } from '../src/benchmarkFormValidation';
import type { BenchmarkFormValues } from '../src/benchmarkFormValidation';
import type { CreateBenchmarkRunRequest } from '../src/benchmarkApi';

afterEach(cleanup);

const target = { k8sCluster: 'main', namespace: 'default', instance: 'mongo-1' };
const input = (label: string) => screen.getByLabelText(label) as HTMLInputElement;
const change = (label: string, value: string) => fireEvent.change(input(label), { target: { value } });
const start = () => screen.getByRole('button', { name: /Start benchmark|Starting…/ }) as HTMLButtonElement;
const load = () => screen.getByRole('checkbox', { name: 'Load benchmark data' }) as HTMLInputElement;

function setup() {
  const submissions: CreateBenchmarkRunRequest[] = [];
  const onSubmit = (values: BenchmarkFormValues) => {
    const request = toCreateBenchmarkRunRequest(values, target, 'percona-server-mongodb');
    if (request) submissions.push(request);
  };
  const form = (isSubmitting: boolean) => (
    <BenchmarkForm target={target} provider="percona-server-mongodb" availableDrivers={['go-ycsb']}
      isSubmitting={isSubmitting} onSubmit={onSubmit} />
  );
  const view = render(form(false));
  return { submissions, pending: (isSubmitting: boolean) => view.rerender(form(isSubmitting)) };
}

describe('go-ycsb options form', () => {
  it('shows a copyable fixed database and defaults loading on with only MongoDB options', () => {
    const { submissions } = setup();
    expect(screen.getByRole('combobox', { name: 'Benchmark driver' }).textContent).toBe('go-ycsb');
    expect(input('Database').value).toBe('ycsb');
    expect(input('Database').readOnly).toBe(true);
    expect(input('Database').disabled).toBe(false);
    expect(screen.getByText(/currently supports only the ycsb database/)).toBeTruthy();
    expect(screen.queryByText(/instance's default database/)).toBeNull();
    expect(screen.getByText(/Workload A/)).toBeTruthy();
    expect(load().checked).toBe(true);
    for (const label of ['Duration (seconds)', 'Clients', 'Scale', 'Tables', 'Rows per table']) {
      expect(screen.queryByLabelText(label)).toBeNull();
    }
    change('Database', 'admin');
    expect(input('Database').value).toBe('ycsb');
    fireEvent.click(start());
    expect(submissions).toEqual([{
      target, database: 'ycsb', driver: 'go-ycsb',
      options: { collection: 'usertable', recordCount: 10000, operationCount: 20000, threads: 1, initialize: true },
    }]);
  });

  it('preserves edited dataset settings and shared resources and placement across loading changes', () => {
    const { submissions } = setup();
    change('Collection', 'benchmark.records');
    change('Records', '500');
    change('Operations', '1000');
    change('Threads', '4');
    expect(screen.getByText(/loading existing keys fails/)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Job resources' }));
    change('CPU request (millicores)', '500');
    change('Memory limit (MiB)', '1024');
    fireEvent.click(screen.getByRole('button', { name: /Job placement/ }));
    fireEvent.click(screen.getByRole('switch', { name: 'Run on nodes with specific labels' }));
    change('Label key 1', 'pool');
    change('Label value 1', 'benchmark');
    fireEvent.click(start());
    const expected = {
      target, database: 'ycsb', driver: 'go-ycsb',
      options: { collection: 'benchmark.records', recordCount: 500, operationCount: 1000, threads: 4, initialize: true },
      resources: { cpuRequest: '500m', memoryLimit: '1024Mi' },
      nodeAffinity: { requiredDuringSchedulingIgnoredDuringExecution: { nodeSelectorTerms: [{
        matchExpressions: [{ key: 'pool', operator: 'In', values: ['benchmark'] }],
      }] } },
    };
    expect(submissions).toEqual([expected]);
    fireEvent.click(load());
    expect(screen.getByText(/Keep Collection and Records matched/)).toBeTruthy();
    for (const label of ['Collection', 'Records', 'Operations', 'Threads']) expect(input(label).disabled).toBe(false);
    fireEvent.click(start());
    expect(submissions[1]).toEqual({ ...expected, options: { ...expected.options, initialize: false } });
  });

  it('validates reused datasets and shows errors after blur or submit', () => {
    const { submissions } = setup();
    fireEvent.click(load());
    change('Collection', '');
    expect(start().disabled).toBe(true);
    expect(screen.queryByText('Enter a collection name.')).toBeNull();
    fireEvent.blur(input('Collection'));
    expect(screen.getByText('Enter a collection name.')).toBeTruthy();
    change('Collection', 'records');
    change('Records', '');
    expect(start().disabled).toBe(true);
    expect(screen.queryByText('Records must be a positive whole number.')).toBeNull();
    fireEvent.submit(start().closest('form')!);
    expect(screen.getByText('Records must be a positive whole number.')).toBeTruthy();
    expect(submissions).toHaveLength(0);
    change('Records', '500');
    change('Operations', '2');
    change('Threads', '3');
    expect(start().disabled).toBe(true);
    expect(screen.getByText('Threads cannot exceed operations.')).toBeTruthy();
    change('Threads', '2');
    expect(start().disabled).toBe(false);
  });

  it('disables all options during submission and keeps the draft afterwards', () => {
    const { submissions, pending } = setup();
    change('Collection', 'existing');
    change('Threads', '4');
    pending(true);
    expect(screen.getByRole('combobox', { name: 'Benchmark driver' }).getAttribute('aria-disabled')).toBe('true');
    for (const label of ['Collection', 'Records', 'Operations', 'Threads']) expect(input(label).disabled).toBe(true);
    expect(load().disabled).toBe(true);
    fireEvent.submit(start().closest('form')!);
    expect(submissions).toHaveLength(0);
    pending(false);
    expect(input('Collection').value).toBe('existing');
    expect(input('Threads').value).toBe('4');
    expect(input('Database').value).toBe('ycsb');
    fireEvent.click(start());
    expect(submissions[0]).toMatchObject({ driver: 'go-ycsb', options: { collection: 'existing', threads: 4 } });
  });
});

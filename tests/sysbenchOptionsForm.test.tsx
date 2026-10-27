import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { BenchmarkForm } from '../src/BenchmarkForm';
import { toCreateBenchmarkRunRequest } from '../src/benchmarkFormValidation';
import type { BenchmarkFormValues } from '../src/benchmarkFormValidation';
import type { CreateBenchmarkRunRequest } from '../src/benchmarkApi';

afterEach(cleanup);

const target = { k8sCluster: 'main', namespace: 'default', instance: 'mysql-1' };
const input = (label: string) => screen.getByLabelText(label) as HTMLInputElement;
const change = (label: string, value: string) => fireEvent.change(input(label), { target: { value } });
const start = () => screen.getByRole('button', { name: /Start benchmark|Starting…/ }) as HTMLButtonElement;
const prepare = () => screen.getByRole('checkbox', { name: 'Prepare Sysbench tables' }) as HTMLInputElement;

function setup() {
  const submissions: CreateBenchmarkRunRequest[] = [];
  const onSubmit = (values: BenchmarkFormValues) => {
    const request = toCreateBenchmarkRunRequest(values, target);
    if (request) submissions.push(request);
  };
  const view = render(<BenchmarkForm target={target} provider="percona-server-mysql" availableDrivers={['sysbench']} isSubmitting={false} onSubmit={onSubmit} />);
  return {
    submissions,
    pending: (isSubmitting: boolean) => view.rerender(
      <BenchmarkForm target={target} provider="percona-server-mysql" availableDrivers={['sysbench']} isSubmitting={isSubmitting} onSubmit={onSubmit} />,
    ),
  };
}

describe('Sysbench options form', () => {
  it('requires an existing database and starts with table preparation enabled', () => {
    const { submissions } = setup();
    const picker = screen.getByRole('combobox', { name: 'Benchmark driver' });
    expect(picker.textContent).toBe('Sysbench');
    fireEvent.mouseDown(picker);
    expect(screen.getAllByRole('option')).toHaveLength(1);
    fireEvent.click(screen.getByRole('option', { name: 'Sysbench' }));
    expect(prepare().checked).toBe(true);
    expect(screen.queryByLabelText('Clients')).toBeNull();
    expect(screen.queryByLabelText('Scale')).toBeNull();
    expect(screen.getByText(/oltp_read_write/)).toBeTruthy();
    expect(screen.getByText(/This provider does not supply a default/)).toBeTruthy();
    expect(input('Database').required).toBe(true);
    expect(start().disabled).toBe(true);
    fireEvent.submit(start().closest('form')!);
    expect(submissions).toHaveLength(0);
    expect(screen.getByText('Enter the name of an existing database.')).toBeTruthy();
    change('Database', '   ');
    expect(start().disabled).toBe(true);
    change('Database', ' sbtest ');
    fireEvent.click(start());
    expect(submissions).toEqual([{
      target, database: 'sbtest', driver: 'sysbench',
      options: { durationSeconds: 30, threads: 1, tables: 1, tableSize: 10000, initialize: true },
    }]);
  });

  it('preserves edited table settings across preparation changes and includes shared settings', () => {
    const { submissions } = setup();
    change('Database', ' sbtest ');
    change('Duration (seconds)', '5');
    change('Threads', '4');
    change('Tables', '2');
    change('Rows per table', '500');
    expect(screen.getByText('Creates benchmark tables in an existing database. Preparation fails if the tables already exist.')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Job resources' }));
    change('CPU request (millicores)', '500');
    change('Memory limit (MiB)', '1024');
    fireEvent.click(screen.getByRole('button', { name: /Job placement/ }));
    fireEvent.click(screen.getByRole('switch', { name: 'Run on nodes with specific labels' }));
    change('Label key 1', 'pool');
    change('Label value 1', 'benchmark');
    fireEvent.click(start());
    const expected = {
      target, database: 'sbtest', driver: 'sysbench',
      options: { durationSeconds: 5, threads: 4, tables: 2, tableSize: 500, initialize: true },
      resources: { cpuRequest: '500m', memoryLimit: '1024Mi' },
      nodeAffinity: { requiredDuringSchedulingIgnoredDuringExecution: { nodeSelectorTerms: [{
        matchExpressions: [{ key: 'pool', operator: 'In', values: ['benchmark'] }],
      }] } },
    };
    expect(submissions).toEqual([expected]);
    fireEvent.click(prepare());
    for (const label of ['Tables', 'Rows per table']) expect(input(label).disabled).toBe(false);
    fireEvent.click(start());
    expect(submissions[1]).toEqual({ ...expected, options: { ...expected.options, initialize: false } });
  });

  it('requires table settings during reuse and reveals errors on blur or submission', () => {
    const { submissions } = setup();
    change('Database', 'sbtest');
    fireEvent.click(prepare());
    change('Tables', '');
    expect(start().disabled).toBe(true);
    expect(screen.queryByText('Tables must be a positive whole number.')).toBeNull();
    fireEvent.blur(input('Tables'));
    expect(screen.getByText('Tables must be a positive whole number.')).toBeTruthy();
    fireEvent.click(prepare());
    expect(start().disabled).toBe(true);
    change('Tables', '2');
    fireEvent.click(prepare());
    change('Rows per table', '0');
    expect(screen.queryByText('Rows per table must be greater than zero.')).toBeNull();
    fireEvent.submit(start().closest('form')!);
    expect(screen.getByText('Rows per table must be greater than zero.')).toBeTruthy();
    expect(submissions).toHaveLength(0);
    change('Rows per table', '500');
    expect(start().disabled).toBe(false);
  });

  it('disables options while submitting and retains values afterwards', () => {
    const { submissions, pending } = setup();
    change('Database', 'sbtest');
    change('Threads', '4');
    pending(true);
    expect(screen.getByRole('combobox', { name: 'Benchmark driver' }).getAttribute('aria-disabled')).toBe('true');
    for (const label of ['Duration (seconds)', 'Threads', 'Tables', 'Rows per table']) {
      expect(input(label).disabled).toBe(true);
    }
    expect(prepare().disabled).toBe(true);
    expect(start().disabled).toBe(true);
    fireEvent.submit(start().closest('form')!);
    expect(submissions).toHaveLength(0);
    pending(false);
    expect(input('Threads').value).toBe('4');
    fireEvent.click(start());
    expect(submissions[0]).toMatchObject({ driver: 'sysbench', options: { threads: 4 } });
  });
});

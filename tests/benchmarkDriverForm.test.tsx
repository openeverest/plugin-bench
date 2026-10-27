import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { BenchmarkForm } from '../src/BenchmarkForm';
import { toCreateBenchmarkRunRequest } from '../src/benchmarkFormValidation';
import type { BenchmarkFormValues } from '../src/benchmarkFormValidation';
import type { CreateBenchmarkRunRequest } from '../src/benchmarkApi';
import type { BenchmarkDriverId } from '../src/drivers';

afterEach(cleanup);

const target = { k8sCluster: 'main', namespace: 'default', instance: 'pg-1' };
const input = (label: string) => screen.getByLabelText(label) as HTMLInputElement;
const change = (label: string, value: string) => fireEvent.change(input(label), { target: { value } });
const start = () => screen.getByRole('button', { name: /Start benchmark|Starting…/ }) as HTMLButtonElement;

function setup(availableDrivers: readonly BenchmarkDriverId[] = ['pgbench']) {
  const submissions: CreateBenchmarkRunRequest[] = [];
  const onSubmit = (values: BenchmarkFormValues) => {
    const request = toCreateBenchmarkRunRequest(values, target);
    if (request) submissions.push(request);
  };
  const view = render(<BenchmarkForm target={target} availableDrivers={availableDrivers} isSubmitting={false} onSubmit={onSubmit} />);
  return {
    submissions,
    pending: (isSubmitting: boolean) => view.rerender(
      <BenchmarkForm target={target} availableDrivers={availableDrivers} isSubmitting={isSubmitting} onSubmit={onSubmit} />,
    ),
    available: (drivers: readonly BenchmarkDriverId[]) => view.rerender(
      <BenchmarkForm target={target} availableDrivers={drivers} isSubmitting={false} onSubmit={onSubmit} />,
    ),
  };
}

describe('benchmark driver form', () => {
  it('shows the registered driver picker and sends its edited options alongside shared fields', () => {
    const { submissions } = setup();
    const picker = screen.getByRole('combobox', { name: 'Benchmark driver' });
    expect(picker.textContent).toBe('pgbench');
    fireEvent.mouseDown(picker);
    expect(screen.getAllByRole('option')).toHaveLength(1);
    fireEvent.click(screen.getByRole('option', { name: 'pgbench' }));
    change('Database', ' app ');
    change('Duration (seconds)', '60');
    change('Clients', '4');
    change('Threads', '2');
    change('Scale', '10');
    fireEvent.click(screen.getByRole('button', { name: 'Job resources' }));
    change('CPU request (millicores)', '500');
    change('Memory limit (MiB)', '1024');
    fireEvent.click(start());
    expect(submissions).toEqual([{
      target, database: 'app', driver: 'pgbench',
      options: { durationSeconds: 60, clients: 4, threads: 2, scale: 10, initialize: true },
      resources: { cpuRequest: '500m', memoryLimit: '1024Mi' },
    }]);
  });

  it('counts option errors immediately but shows each message after blur', () => {
    setup();
    change('Clients', '0');
    expect(start().disabled).toBe(true);
    expect(screen.queryByText('Clients must be greater than zero.')).toBeNull();
    fireEvent.blur(input('Clients'));
    expect(screen.getByText('Clients must be greater than zero.')).toBeTruthy();
    change('Clients', '2');
    expect(start().disabled).toBe(false);
    change('Threads', '3');
    expect(start().disabled).toBe(true);
    expect(screen.queryByText('Threads cannot exceed clients.')).toBeNull();
    fireEvent.blur(input('Threads'));
    expect(screen.getByText('Threads cannot exceed clients.')).toBeTruthy();
  });

  it('ignores the editable scale when reusing tables and validates it when initialization resumes', () => {
    const { submissions } = setup();
    change('Scale', '');
    expect(start().disabled).toBe(true);
    fireEvent.click(screen.getByRole('checkbox', { name: 'Initialize pgbench tables' }));
    expect(input('Scale').disabled).toBe(true);
    expect(start().disabled).toBe(false);
    fireEvent.click(start());
    expect(submissions[0].options).toMatchObject({ scale: 1, initialize: false });
    fireEvent.click(screen.getByRole('checkbox', { name: 'Initialize pgbench tables' }));
    expect(input('Scale').disabled).toBe(false);
    expect(input('Scale').value).toBe('');
    expect(start().disabled).toBe(true);
    change('Scale', '10');
    fireEvent.click(start());
    expect(submissions[1].options).toMatchObject({ scale: 10, initialize: true });
  });

  it('blocks invalid form submission and reveals the driver errors on a submit attempt', () => {
    const { submissions } = setup();
    change('Duration (seconds)', '');
    expect(screen.queryByText('Duration must be a positive whole number.')).toBeNull();
    fireEvent.submit(start().closest('form')!);
    expect(submissions).toHaveLength(0);
    expect(screen.getByText('Duration must be a positive whole number.')).toBeTruthy();
  });

  it('disables driver controls while submitting and retains entered options afterwards', () => {
    const { submissions, pending } = setup();
    change('Clients', '4');
    pending(true);
    expect(screen.getByRole('combobox', { name: 'Benchmark driver' }).getAttribute('aria-disabled')).toBe('true');
    for (const label of ['Duration (seconds)', 'Clients', 'Threads', 'Scale']) {
      expect(input(label).disabled).toBe(true);
    }
    expect(input('Initialize pgbench tables').disabled).toBe(true);
    fireEvent.click(start());
    expect(submissions).toHaveLength(0);
    pending(false);
    expect(input('Clients').value).toBe('4');
    fireEvent.click(start());
    expect(submissions[0]).toMatchObject({ driver: 'pgbench', options: { clients: 4 } });
  });

  it('resets driver options and touched errors while retaining shared fields', () => {
    const { submissions } = setup(['pgbench', 'sysbench']);
    const select = (label: string) => {
      fireEvent.mouseDown(screen.getByRole('combobox', { name: 'Benchmark driver' }));
      fireEvent.click(screen.getByRole('option', { name: label }));
    };
    change('Database', 'app');
    fireEvent.click(screen.getByRole('button', { name: 'Job resources' }));
    change('CPU request (millicores)', '500');
    fireEvent.click(screen.getByRole('button', { name: /Job placement/ }));
    fireEvent.click(screen.getByRole('switch', { name: 'Run on nodes with specific labels' }));
    change('Label key 1', 'pool');
    change('Label value 1', 'benchmark');
    change('Clients', '0');
    fireEvent.blur(input('Clients'));
    expect(screen.getByText('Clients must be greater than zero.')).toBeTruthy();

    select('Sysbench');
    expect(screen.queryByLabelText('Clients')).toBeNull();
    expect(input('Threads').value).toBe('1');
    expect(input('Database').value).toBe('app');
    expect(input('CPU request (millicores)').value).toBe('500');
    expect(input('Label key 1').value).toBe('pool');
    expect(input('Label value 1').value).toBe('benchmark');
    fireEvent.click(start());
    expect(submissions[0]).toMatchObject({
      database: 'app', driver: 'sysbench',
      options: { durationSeconds: 30, threads: 1, tables: 1, tableSize: 10000, initialize: true },
      resources: { cpuRequest: '500m' },
    });
    expect(submissions[0]).toHaveProperty('nodeAffinity');
    expect(submissions[0].options).not.toHaveProperty('clients');
    expect(submissions[0].options).not.toHaveProperty('scale');

    select('pgbench');
    expect(input('Clients').value).toBe('1');
    expect(input('Database').value).toBe('app');
    change('Clients', '0');
    expect(screen.queryByText('Clients must be greater than zero.')).toBeNull();
    change('Clients', '2');
    fireEvent.click(start());
    expect(submissions[1]).toMatchObject({
      database: 'app', driver: 'pgbench', options: { clients: 2, scale: 1 },
      resources: { cpuRequest: '500m' },
    });
    expect(submissions[1]).toHaveProperty('nodeAffinity');
    expect(submissions[1].options).not.toHaveProperty('tables');
  });

  it('uses the selected driver to validate the shared database field', () => {
    setup(['pgbench', 'sysbench']);
    change('Database', 'dbname=app');
    expect(start().disabled).toBe(true);
    fireEvent.mouseDown(screen.getByRole('combobox', { name: 'Benchmark driver' }));
    fireEvent.click(screen.getByRole('option', { name: 'Sysbench' }));
    expect(start().disabled).toBe(false);
  });

  it('blocks submission when the driver is no longer available', () => {
    const { submissions, available } = setup();
    available(['sysbench']);
    expect(start().disabled).toBe(true);
    fireEvent.submit(start().closest('form')!);
    expect(submissions).toHaveLength(0);
    expect(screen.queryByLabelText('Clients')).toBeNull();
    fireEvent.mouseDown(screen.getByRole('combobox', { name: 'Benchmark driver' }));
    expect(screen.getAllByRole('option')).toHaveLength(1);
    fireEvent.click(screen.getByRole('option', { name: 'Sysbench' }));
    fireEvent.click(start());
    expect(submissions[0]).toMatchObject({ driver: 'sysbench' });
  });

  it('blocks submission with an empty available-driver list', () => {
    const { submissions } = setup([]);
    expect(start().disabled).toBe(true);
    expect(screen.getByRole('combobox', { name: 'Benchmark driver' }).getAttribute('aria-disabled')).toBe('true');
    fireEvent.submit(start().closest('form')!);
    expect(submissions).toHaveLength(0);
    expect(screen.queryByLabelText('Clients')).toBeNull();
    expect(screen.queryByLabelText('Tables')).toBeNull();
  });
});

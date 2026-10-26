import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { BenchmarkForm } from '../src/BenchmarkForm';
import { toCreateBenchmarkRunRequest } from '../src/benchmarkFormValidation';
import type { CreateBenchmarkRunRequest } from '../src/benchmarkApi';
import type { BenchmarkTarget } from '../src/benchmarkTarget';

afterEach(cleanup);

const target: BenchmarkTarget = { k8sCluster: 'main', namespace: 'default', instance: 'pg-1' };

function setup(isSubmitting = false) {
  const submissions: CreateBenchmarkRunRequest[] = [];
  const onSubmit = (values: Parameters<typeof toCreateBenchmarkRunRequest>[0]) => {
    const request = toCreateBenchmarkRunRequest(values, target);
    if (request) submissions.push(request);
  };
  const view = render(<BenchmarkForm target={target} availableDrivers={['pgbench']} isSubmitting={isSubmitting} onSubmit={onSubmit} />);
  return {
    submissions,
    rerender: (pending: boolean) => view.rerender(
      <BenchmarkForm target={target} availableDrivers={['pgbench']} isSubmitting={pending} onSubmit={onSubmit} />,
    ),
  };
}

const placement = () => screen.getByRole('button', { name: /Job placement/ });
const affinitySwitch = () => screen.getByRole('switch', { name: 'Run on nodes with specific labels' }) as HTMLInputElement;
const enableAffinity = () => { fireEvent.click(placement()); fireEvent.click(affinitySwitch()); };
const startButton = () => screen.getByRole('button', { name: /Start benchmark|Starting…/ });
const submit = () => fireEvent.click(startButton());
const input = (label: string) => screen.getByLabelText(label) as HTMLInputElement;
const change = (label: string, value: string) => fireEvent.change(input(label), { target: { value } });

describe('benchmark node affinity form', () => {
  it('omits affinity for an ordinary run', () => {
    const { submissions } = setup();
    submit();
    expect(submissions).toHaveLength(1);
    expect(submissions[0]).not.toHaveProperty('nodeAffinity');
  });

  it('submits all label rows as one required term', () => {
    const { submissions } = setup();
    enableAffinity();
    expect(startButton().hasAttribute('disabled')).toBe(true);
    change('Label key 1', 'pool');
    change('Label value 1', 'benchmark');
    fireEvent.click(screen.getByRole('button', { name: 'Add label' }));
    change('Label key 2', 'disk');
    change('Label value 2', 'ssd');
    submit();
    expect(submissions).toHaveLength(1);
    expect(submissions[0].nodeAffinity).toEqual({
      requiredDuringSchedulingIgnoredDuringExecution: { nodeSelectorTerms: [{ matchExpressions: [
        { key: 'pool', operator: 'In', values: ['benchmark'] },
        { key: 'disk', operator: 'In', values: ['ssd'] },
      ] }] },
    });
  });

  it('shows field errors only after the field is left, but always counts them', () => {
    setup();
    enableAffinity();
    expect(screen.getByText('2 placement errors')).toBeTruthy();
    expect(screen.queryByText('Enter a label key.')).toBeNull();
    fireEvent.blur(input('Label key 1'));
    expect(screen.getByText('Enter a label key.')).toBeTruthy();
    expect(screen.queryByText('Enter a label value.')).toBeNull();
  });

  it('keeps each row\'s values and errors attached to it when an earlier row is removed', () => {
    const { submissions } = setup();
    enableAffinity();
    change('Label key 1', 'pool');
    change('Label value 1', 'benchmark');
    fireEvent.click(screen.getByRole('button', { name: 'Add label' }));
    change('Label key 2', 'disk');
    fireEvent.blur(input('Label value 2'));
    expect(screen.getByText('Enter a label value.')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Remove label 1' }));
    expect(input('Label key 1').value).toBe('disk');
    expect(screen.getByText('Enter a label value.')).toBeTruthy();
    change('Label value 1', 'ssd');
    submit();
    expect(submissions[0].nodeAffinity?.requiredDuringSchedulingIgnoredDuringExecution?.nodeSelectorTerms)
      .toEqual([{ matchExpressions: [{ key: 'disk', operator: 'In', values: ['ssd'] }] }]);
  });

  it('keeps entered rows when switched off and on again, and disables controls while submitting', () => {
    const { submissions, rerender } = setup();
    enableAffinity();
    change('Label key 1', 'pool');
    fireEvent.click(affinitySwitch());
    submit();
    expect(submissions[0]).not.toHaveProperty('nodeAffinity');
    fireEvent.click(affinitySwitch());
    expect(input('Label key 1').value).toBe('pool');
    rerender(true);
    expect(affinitySwitch().disabled).toBe(true);
    expect(input('Label key 1').disabled).toBe(true);
  });
});

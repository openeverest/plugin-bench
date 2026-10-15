import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
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
  const view = render(<BenchmarkForm target={target} isSubmitting={isSubmitting} onSubmit={onSubmit} />);
  return {
    submissions,
    rerender: (pending: boolean) => view.rerender(
      <BenchmarkForm target={target} isSubmitting={pending} onSubmit={onSubmit} />,
    ),
  };
}

const placement = () => screen.getByRole('button', { name: /Job placement/ });
const openPlacement = () => fireEvent.click(placement());
const enableAffinity = () => fireEvent.click(screen.getByRole('switch', { name: 'Configure node affinity' }));
const startButton = () => screen.getByRole('button', { name: /Start benchmark|Starting…/ });
const submit = () => fireEvent.click(startButton());
const click = (name: string) => fireEvent.click(screen.getByRole('button', { name }));
const change = (input: HTMLElement, value: string) => fireEvent.change(input, { target: { value } });
const group = (kind: 'Required' | 'Preferred', number: number) =>
  within(screen.getByRole('group', { name: `${kind} group ${number}` }));
const field = (kind: 'Required' | 'Preferred', number: number, label: string, index = 0) =>
  group(kind, number).getAllByLabelText(label)[index];

describe('benchmark node affinity form', () => {
  it('omits affinity for an ordinary run and starts collapsed', () => {
    const { submissions } = setup();
    expect(placement().getAttribute('aria-expanded')).toBe('false');
    expect(startButton().hasAttribute('disabled')).toBe(false);
    openPlacement();
    expect((screen.getByRole('switch', { name: 'Configure node affinity' }) as HTMLInputElement).checked).toBe(false);
    expect(screen.queryByLabelText('Label key')).toBeNull();
    submit();
    expect(submissions).toHaveLength(1);
    expect(submissions[0]).not.toHaveProperty('nodeAffinity');
  });

  it('submits required AND/OR groups, values, preferences, and runner resources', () => {
    const { submissions } = setup();
    openPlacement();
    enableAffinity();
    expect(startButton().hasAttribute('disabled')).toBe(true);
    submit();
    expect(submissions).toHaveLength(0);
    click('Add required group');
    change(field('Required', 1, 'Label key'), 'workload');
    change(field('Required', 1, 'Value 1'), 'benchmark');
    fireEvent.click(group('Required', 1).getByRole('button', { name: 'Add condition' }));
    change(field('Required', 1, 'Label key', 1), 'zone');
    change(field('Required', 1, 'Value 1', 1), 'east');
    fireEvent.click(group('Required', 1).getAllByRole('button', { name: 'Add value' })[1]);
    change(field('Required', 1, 'Value 2'), 'west');
    click('Add required group');
    change(field('Required', 2, 'Label key'), 'pool');
    change(field('Required', 2, 'Value 1'), 'batch');
    click('Add preferred group');
    change(field('Preferred', 1, 'Label key'), 'disk');
    change(field('Preferred', 1, 'Value 1'), 'ssd');
    change(field('Preferred', 1, 'Weight'), '75');
    fireEvent.click(screen.getByRole('button', { name: /Job resources/ }));
    change(screen.getByLabelText('CPU request (millicores)'), '500');
    change(screen.getByLabelText('Memory limit (MiB)'), '1024');
    expect(startButton().hasAttribute('disabled')).toBe(false);
    submit();
    expect(submissions).toHaveLength(1);
    expect(submissions[0].nodeAffinity).toEqual({
      requiredDuringSchedulingIgnoredDuringExecution: { nodeSelectorTerms: [
        { matchExpressions: [
          { key: 'workload', operator: 'In', values: ['benchmark'] },
          { key: 'zone', operator: 'In', values: ['east', 'west'] },
        ] },
        { matchExpressions: [{ key: 'pool', operator: 'In', values: ['batch'] }] },
      ] },
      preferredDuringSchedulingIgnoredDuringExecution: [{ weight: 75, preference: {
        matchExpressions: [{ key: 'disk', operator: 'In', values: ['ssd'] }],
      } }],
    });
    expect(submissions[0].resources).toEqual({ cpuRequest: '500m', memoryLimit: '1024Mi' });
  });

  it('preserves invalid drafts on collapse and while disabled', () => {
    const { submissions } = setup();
    openPlacement();
    enableAffinity();
    click('Add preferred group');
    change(field('Preferred', 1, 'Label key'), 'disk');
    change(field('Preferred', 1, 'Value 1'), 'ssd');
    change(field('Preferred', 1, 'Weight'), '101');
    expect(startButton().hasAttribute('disabled')).toBe(true);
    expect(screen.getByText('Weight must be a whole number from 1 to 100.')).toBeTruthy();
    openPlacement();
    expect(placement().getAttribute('aria-expanded')).toBe('false');
    expect(startButton().hasAttribute('disabled')).toBe(true);
    openPlacement();
    enableAffinity();
    expect(startButton().hasAttribute('disabled')).toBe(false);
    submit();
    expect(submissions[0]).not.toHaveProperty('nodeAffinity');
    enableAffinity();
    expect((field('Preferred', 1, 'Weight') as HTMLInputElement).value).toBe('101');
    expect(startButton().hasAttribute('disabled')).toBe(true);
  });

  it('clears hidden values when the operator changes and accepts numeric thresholds', () => {
    const { submissions } = setup();
    openPlacement();
    enableAffinity();
    click('Add required group');
    change(field('Required', 1, 'Label key'), 'generation');
    change(field('Required', 1, 'Value 1'), 'ssd');
    fireEvent.click(group('Required', 1).getByRole('button', { name: 'Add value' }));
    change(field('Required', 1, 'Value 2'), 'nvme');
    fireEvent.mouseDown(field('Required', 1, 'Operator'));
    fireEvent.click(screen.getByRole('option', { name: 'Exists', hidden: true }));
    expect(group('Required', 1).queryByLabelText('Value 1')).toBeNull();
    expect(startButton().hasAttribute('disabled')).toBe(false);
    fireEvent.mouseDown(field('Required', 1, 'Operator'));
    fireEvent.click(screen.getByRole('option', { name: /Gt \(greater than\)/, hidden: true }));
    expect(group('Required', 1).getAllByLabelText('Integer threshold')).toHaveLength(1);
    expect(startButton().hasAttribute('disabled')).toBe(true);
    change(field('Required', 1, 'Integer threshold'), '9223372036854775807');
    expect(startButton().hasAttribute('disabled')).toBe(false);
    submit();
    expect(submissions[0].nodeAffinity?.requiredDuringSchedulingIgnoredDuringExecution?.nodeSelectorTerms[0]
      .matchExpressions?.[0]).toEqual({ key: 'generation', operator: 'Gt', values: ['9223372036854775807'] });
    fireEvent.mouseDown(field('Required', 1, 'Operator'));
    fireEvent.click(screen.getByRole('option', { name: 'DoesNotExist', hidden: true }));
    expect(group('Required', 1).queryByLabelText('Integer threshold')).toBeNull();
  });

  it('shows errors when values, conditions, or groups are removed', () => {
    setup();
    openPlacement();
    enableAffinity();
    click('Add required group');
    change(field('Required', 1, 'Label key'), 'workload');
    click('Remove value 1 from required group 1 condition 1');
    expect(startButton().hasAttribute('disabled')).toBe(true);
    expect(screen.getByText('Enter at least one value.')).toBeTruthy();
    fireEvent.click(group('Required', 1).getByRole('button', { name: 'Add value' }));
    change(field('Required', 1, 'Value 1'), 'benchmark');
    expect(startButton().hasAttribute('disabled')).toBe(false);
    click('Remove required group 1 condition 1');
    expect(startButton().hasAttribute('disabled')).toBe(true);
    click('Remove required group 1');
    expect(screen.getByText('Add at least one required or preferred group.')).toBeTruthy();
  });

  it('disables placement controls while submitting and keeps two forms independent', () => {
    const first = setup();
    openPlacement();
    enableAffinity();
    click('Add required group');
    change(field('Required', 1, 'Label key'), 'workload');
    first.rerender(true);
    expect((screen.getByRole('switch', { name: 'Configure node affinity' }) as HTMLInputElement).disabled).toBe(true);
    expect((field('Required', 1, 'Label key') as HTMLInputElement).disabled).toBe(true);
    expect(screen.getByRole('button', { name: 'Add required group' }).hasAttribute('disabled')).toBe(true);
    expect(startButton().hasAttribute('disabled')).toBe(true);
    cleanup();
    setup();
    openPlacement();
    expect((screen.getByRole('switch', { name: 'Configure node affinity' }) as HTMLInputElement).checked).toBe(false);
    expect(screen.queryByLabelText('Label key')).toBeNull();
  });

  it('keeps collapsed placement errors visible and connects the accordion to its region', () => {
    setup();
    openPlacement();
    enableAffinity();
    openPlacement();
    expect(placement().getAttribute('aria-expanded')).toBe('false');
    expect(screen.getByText('1 placement error')).toBeTruthy();
    const controlledId = placement().getAttribute('aria-controls');
    expect(controlledId).toBeTruthy();
    const region = document.getElementById(controlledId!);
    expect(region?.getAttribute('role')).toBe('region');
    expect(region?.getAttribute('aria-labelledby')).toBe(placement().id);
  });

  it('keeps the remaining preferred group after removing the first', () => {
    const { submissions } = setup();
    openPlacement();
    enableAffinity();
    click('Add preferred group');
    change(field('Preferred', 1, 'Label key'), 'disk');
    change(field('Preferred', 1, 'Value 1'), 'ssd');
    change(field('Preferred', 1, 'Weight'), '25');
    click('Add preferred group');
    change(field('Preferred', 2, 'Label key'), 'pool');
    change(field('Preferred', 2, 'Value 1'), 'batch');
    change(field('Preferred', 2, 'Weight'), '100');
    click('Remove preferred group 1');
    expect((field('Preferred', 1, 'Label key') as HTMLInputElement).value).toBe('pool');
    expect((field('Preferred', 1, 'Weight') as HTMLInputElement).value).toBe('100');
    submit();
    expect(submissions[0].nodeAffinity).toEqual({ preferredDuringSchedulingIgnoredDuringExecution: [{
      weight: 100, preference: { matchExpressions: [{ key: 'pool', operator: 'In', values: ['batch'] }] },
    }] });
  });
});

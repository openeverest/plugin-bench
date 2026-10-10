import assert from 'node:assert/strict';
import test from 'node:test';
import React from 'react';
import renderer from 'react-test-renderer';
import { JSDOM } from 'jsdom';
import { toCreateBenchmarkRunRequest } from '../dist/form-test/benchmarkFormValidation.js';

// Install the DOM before importing MUI/Emotion so refs use their client behavior.
const dom = new JSDOM('<!doctype html><html><head></head><body></body></html>');
globalThis.window = dom.window;
globalThis.document = dom.window.document;
const { Accordion, Button, Switch, TextField, Chip } = await import('@mui/material');
const { CacheProvider } = await import('@emotion/react');
const { default: createCache } = await import('@emotion/cache');
const { BenchmarkForm } = await import('../dist/form-test/BenchmarkForm.js');
const { BenchmarkNodeAffinityForm } = await import('../dist/form-test/BenchmarkNodeAffinityForm.js');
test.after(() => { dom.window.close(); delete globalThis.window; delete globalThis.document; });

const { act } = renderer;
const target = { k8sCluster: 'main', namespace: 'default', instance: 'pg-1' };
// MUI's Collapse and Select use DOM refs. Return detached DOM elements;
// tests drive the public component callbacks, not simulated browser layout.
const createNodeMock = element => document.createElement(element.type);

function setup(t, isSubmitting = false) {
  const submissions = [];
  const cache = createCache({ key: 'affinity-test', container: document.head });
  const element = pending => React.createElement(CacheProvider, { value: cache }, React.createElement(BenchmarkForm, {
    target, isSubmitting: pending, onSubmit: values => submissions.push(toCreateBenchmarkRunRequest(values, target)),
  }));
  let view;
  act(() => { view = renderer.create(element(isSubmitting), { createNodeMock }); });
  t.after(() => { act(() => view.unmount()); cache.sheet.flush(); });
  return { view, submissions, setSubmitting: pending => act(() => view.update(element(pending))) };
}
const editor = view => view.root.findByType(BenchmarkNodeAffinityForm);
const button = (view, label) => view.root.findAllByType(Button).find(item => item.props.children === label);
const fields = (view, label) => view.root.findAllByType(TextField).filter(item => item.props.label === label);
const click = (view, label) => act(() => button(view, label).props.onClick());
const change = (field, value) => act(() => field.props.onChange({ target: { value } }));
const enable = (view, checked) => act(() => view.root.findByType(Switch).props.onChange({ target: { checked } }));
const collapse = (view, expanded) => act(() => view.root.findByType(Accordion).props.onChange({}, expanded));
const submit = view => act(() => view.root.findByType('form').props.onSubmit({ preventDefault() {} }));
const remove = (view, label) => act(() => view.root.findAllByType(Button)
  .find(item => item.props['aria-label'] === label).props.onClick());

test('placement is off and collapsed by default; ordinary submissions omit affinity', t => {
  const { view, submissions } = setup(t);
  assert.equal(view.root.findByType(Accordion).props.expanded, false);
  assert.equal(view.root.findByType(Switch).props.checked, false);
  assert.equal(button(view, 'Start benchmark').props.disabled, false);
  assert.equal(fields(view, 'Label key').length, 0);
  submit(view);
  assert.equal(submissions.length, 1);
  assert.equal('nodeAffinity' in submissions[0], false);
});

test('editing required AND/OR groups, multiple values, and weighted preferences reaches submission', t => {
  const { view, submissions } = setup(t);
  collapse(view, true);
  enable(view, true);
  assert.equal(button(view, 'Start benchmark').props.disabled, true);
  submit(view);
  assert.equal(submissions.length, 0);
  click(view, 'Add required group');
  change(fields(view, 'Label key')[0], 'workload');
  change(fields(view, 'Value 1')[0], 'benchmark');
  click(view, 'Add condition');
  change(fields(view, 'Label key')[1], 'zone');
  change(fields(view, 'Value 1')[1], 'east');
  // Add a second value to the second condition.
  act(() => view.root.findAllByType(Button).filter(item => item.props.children === 'Add value')[1].props.onClick());
  change(fields(view, 'Value 2')[0], 'west');
  click(view, 'Add required group');
  change(fields(view, 'Label key')[2], 'pool');
  change(fields(view, 'Value 1')[2], 'batch');
  click(view, 'Add preferred group');
  change(fields(view, 'Label key')[3], 'disk');
  change(fields(view, 'Value 1')[3], 'ssd');
  change(fields(view, 'Weight')[0], '75');
  assert.equal(button(view, 'Start benchmark').props.disabled, false);
  submit(view);
  assert.deepEqual(submissions[0].nodeAffinity, {
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
});

test('collapse and the enable switch preserve drafts; invalid rules block only enabled submissions', t => {
  const { view, submissions } = setup(t);
  collapse(view, true);
  enable(view, true);
  click(view, 'Add preferred group');
  change(fields(view, 'Label key')[0], 'disk');
  change(fields(view, 'Value 1')[0], 'ssd');
  change(fields(view, 'Weight')[0], '101');
  assert.equal(fields(view, 'Weight')[0].props.error, true);
  assert.ok(editor(view).props.errors['preferred[0].weight']);
  const draft = structuredClone(editor(view).props.draft);
  collapse(view, false);
  assert.deepEqual(editor(view).props.draft, draft);
  assert.equal(button(view, 'Start benchmark').props.disabled, true);
  submit(view);
  assert.equal(submissions.length, 0);
  enable(view, false);
  assert.deepEqual(editor(view).props.draft.preferred, draft.preferred);
  assert.equal(button(view, 'Start benchmark').props.disabled, false);
  submit(view);
  assert.equal('nodeAffinity' in submissions[0], false);
  enable(view, true);
  collapse(view, true);
  assert.equal(fields(view, 'Weight')[0].props.value, '101');
  assert.equal(button(view, 'Start benchmark').props.disabled, true);
});

test('operator changes clear hidden values and show a single numeric threshold', t => {
  const { view, submissions } = setup(t);
  enable(view, true);
  click(view, 'Add required group');
  change(fields(view, 'Label key')[0], 'generation');
  change(fields(view, 'Value 1')[0], 'ssd');
  click(view, 'Add value');
  change(fields(view, 'Value 2')[0], 'nvme');
  change(fields(view, 'Operator')[0], 'Exists');
  assert.deepEqual(editor(view).props.draft.required[0].matchExpressions[0].values, []);
  assert.equal(fields(view, 'Value 1').length, 0);
  assert.equal(button(view, 'Add value'), undefined);
  assert.equal(button(view, 'Start benchmark').props.disabled, false);
  change(fields(view, 'Operator')[0], 'Gt');
  assert.equal(fields(view, 'Integer threshold').length, 1);
  assert.equal(button(view, 'Start benchmark').props.disabled, true);
  change(fields(view, 'Integer threshold')[0], '9223372036854775807');
  assert.equal(button(view, 'Start benchmark').props.disabled, false);
  submit(view);
  assert.deepEqual(submissions[0].nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[0].matchExpressions[0],
    { key: 'generation', operator: 'Gt', values: ['9223372036854775807'] });
  change(fields(view, 'Operator')[0], 'DoesNotExist');
  assert.deepEqual(editor(view).props.draft.required[0].matchExpressions[0].values, []);
});

test('removing values, conditions, and groups exposes errors instead of silently dropping rules', t => {
  const { view } = setup(t);
  enable(view, true);
  click(view, 'Add required group');
  change(fields(view, 'Label key')[0], 'workload');
  remove(view, 'Remove value 1 from required group 1 condition 1');
  assert.ok(editor(view).props.errors['required[0].matchExpressions[0].values']);
  assert.equal(button(view, 'Start benchmark').props.disabled, true);
  click(view, 'Add value');
  change(fields(view, 'Value 1')[0], 'benchmark');
  assert.equal(button(view, 'Start benchmark').props.disabled, false);
  remove(view, 'Remove required group 1 condition 1');
  assert.ok(editor(view).props.errors['required[0]']);
  remove(view, 'Remove required group 1');
  assert.ok(editor(view).props.errors.nodeAffinity);
  assert.equal(button(view, 'Start benchmark').props.disabled, true);
});

test('pending submission disables placement controls and two forms own independent drafts', t => {
  const first = setup(t);
  enable(first.view, true);
  click(first.view, 'Add required group');
  change(fields(first.view, 'Label key')[0], 'workload');
  first.setSubmitting(true);
  assert.equal(first.view.root.findByType(Switch).props.disabled, true);
  for (const field of fields(first.view, 'Label key')) assert.equal(field.props.disabled, true);
  assert.equal(button(first.view, 'Add required group').props.disabled, true);
  assert.equal(button(first.view, 'Starting…').props.disabled, true);
  for (const item of first.view.root.findAllByType(Button)) {
    if (item.props.children !== 'Starting…') assert.equal(item.props.type, 'button');
  }
  const second = setup(t);
  assert.deepEqual(editor(second.view).props.draft, { enabled: false, required: [], preferred: [] });
});

test('collapsed placement errors remain discoverable', t => {
  const { view } = setup(t);
  enable(view, true);
  assert.equal(view.root.findByType(Accordion).props.expanded, false);
  assert.ok(view.root.findAllByType(Chip).some(item => item.props.label === '1 placement error'));
  const summary = view.root.findAllByType('button').find(item => item.props['aria-expanded'] === false);
  const controlledId = summary.props['aria-controls'];
  assert.ok(controlledId);
  const regions = view.root.findAll(item => typeof item.type === 'string' && item.props.id === controlledId);
  assert.equal(regions.length, 1, 'The accordion control must identify one region, not duplicate IDs');
  assert.equal(regions[0].props.role, 'region');
  assert.equal(regions[0].props['aria-labelledby'], summary.props.id);
});

test('removing a preferred group keeps the remaining group’s weight and conditions', t => {
  const { view, submissions } = setup(t);
  enable(view, true);
  click(view, 'Add preferred group');
  change(fields(view, 'Label key')[0], 'disk');
  change(fields(view, 'Value 1')[0], 'ssd');
  change(fields(view, 'Weight')[0], '25');
  click(view, 'Add preferred group');
  change(fields(view, 'Label key')[1], 'pool');
  change(fields(view, 'Value 1')[1], 'batch');
  change(fields(view, 'Weight')[1], '100');
  remove(view, 'Remove preferred group 1');
  assert.equal(fields(view, 'Label key')[0].props.value, 'pool');
  assert.equal(fields(view, 'Weight')[0].props.value, '100');
  submit(view);
  assert.deepEqual(submissions[0].nodeAffinity, { preferredDuringSchedulingIgnoredDuringExecution: [{
    weight: 100, preference: { matchExpressions: [{ key: 'pool', operator: 'In', values: ['batch'] }] },
  }] });
});

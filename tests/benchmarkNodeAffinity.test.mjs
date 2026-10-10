import assert from 'node:assert/strict';
import test from 'node:test';
import {
  createNodeAffinityDraft, isNodeAffinity, prepareNodeAffinityDraft, validateNodeAffinity,
} from '../dist/form-test/benchmarkNodeAffinity.js';

const condition = (key = 'workload', operator = 'In', values = ['benchmark']) => ({ key, operator, values });
const draft = () => ({
  enabled: true,
  required: [
    { matchExpressions: [condition(), condition('generation', 'Gt', ['3'])] },
    { matchExpressions: [condition('example.com/pool', 'In', ['batch', 'compute'])] },
  ],
  preferred: [{ weight: '75', matchExpressions: [condition('disk', 'In', ['ssd'])] }],
});

test('disabled drafts omit affinity without destroying entered rules; initial drafts are independent', () => {
  const input = draft();
  input.enabled = false;
  input.preferred[0].weight = 'invalid';
  const before = structuredClone(input);
  assert.deepEqual(prepareNodeAffinityDraft(input), { ok: true });
  assert.deepEqual(input, before);
  assert.deepEqual(prepareNodeAffinityDraft(), { ok: true });
  const first = createNodeAffinityDraft();
  first.required.push({ matchExpressions: [condition()] });
  assert.deepEqual(createNodeAffinityDraft(), { enabled: false, required: [], preferred: [] });
});

test('conversion preserves AND conditions, OR groups, weighted preferences, and multiple values', () => {
  const input = draft();
  const before = structuredClone(input);
  const prepared = prepareNodeAffinityDraft(input);
  assert.deepEqual(prepared, { ok: true, nodeAffinity: {
    requiredDuringSchedulingIgnoredDuringExecution: { nodeSelectorTerms: before.required },
    preferredDuringSchedulingIgnoredDuringExecution: [{ weight: 75, preference: {
      matchExpressions: [condition('disk', 'In', ['ssd'])],
    } }],
  } });
  assert.deepEqual(input, before);
  prepared.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[0].matchExpressions[0].values[0] = 'changed';
  prepared.nodeAffinity.preferredDuringSchedulingIgnoredDuringExecution[0].preference.matchExpressions[0].key = 'changed';
  assert.deepEqual(input, before);
});

test('required-only and preferred-only drafts omit the unused native section', () => {
  for (const kind of ['required', 'preferred']) {
    const input = draft();
    input[kind === 'required' ? 'preferred' : 'required'] = [];
    const result = prepareNodeAffinityDraft(input);
    assert.equal(result.ok, true);
    assert.deepEqual(Object.keys(result.nodeAffinity), [kind === 'required'
      ? 'requiredDuringSchedulingIgnoredDuringExecution' : 'preferredDuringSchedulingIgnoredDuringExecution']);
  }
});

test('invalid enabled drafts provide paths and never return a partial payload', () => {
  const invalid = [
    [{ enabled: true, required: [], preferred: [] }, 'nodeAffinity'],
    [{ enabled: true, required: [{ matchExpressions: [] }], preferred: [] }, 'required[0]'],
    [{ enabled: true, required: [{ matchExpressions: [condition('bad/key/extra')] }], preferred: [] }, 'required[0].matchExpressions[0].key'],
    [{ enabled: true, required: [], preferred: [{ weight: '75', matchExpressions: [] }] }, 'preferred[0]'],
  ];
  for (const weight of ['', '0', '101', '1.5', 'abc', '1e2', '0x10', 'Infinity']) {
    const input = draft();
    input.preferred[0].weight = weight;
    invalid.push([input, 'preferred[0].weight']);
  }
  for (const [input, path] of invalid) {
    const result = prepareNodeAffinityDraft(input);
    assert.equal(result.ok, false, JSON.stringify(input));
    assert.ok(result.errors[path], path);
    assert.equal('nodeAffinity' in result, false);
  }
});

const native = requirement => ({
  requiredDuringSchedulingIgnoredDuringExecution: { nodeSelectorTerms: [{ matchExpressions: [requirement] }] },
});

test('all label operators follow cardinality, label syntax, and int64 threshold rules', () => {
  const valid = [
    condition(), condition('example.com/Pool_Name', 'NotIn', ['ssd', '']),
    condition('gpu', 'Exists', []), { key: 'gpu', operator: 'DoesNotExist' },
    condition('generation', 'Gt', ['9223372036854775807']),
    condition('generation', 'Lt', ['0']), condition('generation', 'Gt', ['0003']),
    condition('a'.repeat(63), 'In', ['v'.repeat(63)]),
  ];
  const invalid = [
    condition('', 'In', ['x']), condition('Bad.Example/key'), condition('bad key'),
    condition('example.com/'), condition('a'.repeat(64)), condition('workload', 'Invalid'),
    condition('workload', 'In', []), { key: 'workload', operator: 'NotIn' },
    condition('gpu', 'Exists', ['yes']), condition('gpu', 'DoesNotExist', ['yes']),
    condition('generation', 'Gt', []), condition('generation', 'Lt', ['1', '2']),
    ...['-1', '+1', '1.5', '1e2', '', '9223372036854775808'].map(value => condition('generation', 'Gt', [value])),
    condition('disk', 'In', ['bad value']), condition('disk', 'In', ['v'.repeat(64)]),
    condition('disk', 'In', ['ssd\n']), condition('workload\n'),
  ];
  for (const item of valid) assert.deepEqual(validateNodeAffinity(native(item)), {}, JSON.stringify(item));
  for (const item of invalid) assert.equal(isNodeAffinity(native(item)), false, JSON.stringify(item));
});

test('native snapshots accept empty affinity and field selectors in required and preferred groups', () => {
  for (const affinity of [{}, { preferredDuringSchedulingIgnoredDuringExecution: [] }]) {
    assert.equal(isNodeAffinity(affinity), true);
  }
  const term = { matchFields: [{ key: 'metadata.name', operator: 'NotIn', values: ['node-1.example'] }] };
  assert.equal(isNodeAffinity({
    requiredDuringSchedulingIgnoredDuringExecution: { nodeSelectorTerms: [term] },
    preferredDuringSchedulingIgnoredDuringExecution: [{ weight: 100, preference: term }],
  }), true);
  for (const requirement of [
    { key: 'metadata.namespace', operator: 'In', values: ['node-1'] },
    { key: 'metadata.name', operator: 'Exists' },
    ...[[], ['node-1', 'node-2'], ['BAD'], ['node name'], ['node-1\n'], ['']]
      .map(values => ({ key: 'metadata.name', operator: 'In', values })),
  ]) {
    assert.equal(isNodeAffinity({ requiredDuringSchedulingIgnoredDuringExecution: {
      nodeSelectorTerms: [{ matchFields: [requirement] }],
    } }), false, JSON.stringify(requirement));
  }
});

test('untrusted snapshots reject malformed nested structures, types, and unknown fields', () => {
  const required = value => ({ requiredDuringSchedulingIgnoredDuringExecution: value });
  const preferred = value => ({ preferredDuringSchedulingIgnoredDuringExecution: value });
  const cases = [null, [], 'affinity', { nodeSelector: {} }, required(null), required({}),
    required({ nodeSelectorTerms: [] }), required({ nodeSelectorTerms: {} }),
    required({ nodeSelectorTerms: [null] }), required({ nodeSelectorTerms: [{}] }),
    required({ nodeSelectorTerms: [{ matchExpressions: {} }] }),
    required({ nodeSelectorTerms: [{ matchFields: null }] }),
    native({ ...condition(), values: 'benchmark' }), native({ ...condition(), values: [1] }),
    native({ ...condition(), extra: true }), native({ ...condition(), key: 123 }),
    preferred(null), preferred({}), preferred([null]), preferred([{ weight: 50 }]),
    ...[0, 101, 1.5, '50', NaN, Infinity].map(weight => preferred([{ weight, preference: { matchExpressions: [condition()] } }])),
    preferred([{ weight: 50, preference: { matchExpressions: [condition()], extra: true } }]),
  ];
  for (const value of cases) assert.equal(isNodeAffinity(value), false, JSON.stringify(value));
});

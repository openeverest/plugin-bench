import assert from 'node:assert/strict';
import test from 'node:test';
import {
  createNodeAffinityDraft, createNodeLabelRow, isNodeAffinity, prepareNodeAffinityDraft,
} from '../dist/form-test/benchmarkNodeAffinity.js';

const row = (key, value) => ({ ...createNodeLabelRow(), key, value });

test('disabled drafts omit affinity without discarding entered rows', () => {
  const draft = { enabled: false, rows: [row('', '')] };
  const before = structuredClone(draft);
  assert.deepEqual(prepareNodeAffinityDraft(draft), { ok: true });
  assert.deepEqual(draft, before);
  assert.deepEqual(prepareNodeAffinityDraft(createNodeAffinityDraft()), { ok: true });
});

test('rows become one required term whose label conditions must all match', () => {
  const result = prepareNodeAffinityDraft({ enabled: true, rows: [row(' pool ', ' benchmark '), row('disk', 'ssd')] });
  assert.deepEqual(result, { ok: true, nodeAffinity: {
    requiredDuringSchedulingIgnoredDuringExecution: { nodeSelectorTerms: [{ matchExpressions: [
      { key: 'pool', operator: 'In', values: ['benchmark'] },
      { key: 'disk', operator: 'In', values: ['ssd'] },
    ] }] },
  } });
});

test('blank keys, blank values, duplicate keys, and empty drafts are rejected by row ID', () => {
  assert.deepEqual(prepareNodeAffinityDraft({ enabled: true, rows: [] }),
    { ok: false, errors: { nodeAffinity: 'Add at least one node label.' } });

  const blankKey = row(' ', 'x');
  const blankValue = row('pool', ' ');
  const duplicate = row('pool', 'other');
  const result = prepareNodeAffinityDraft({ enabled: true, rows: [blankKey, blankValue, duplicate] });
  assert.equal(result.ok, false);
  assert.deepEqual(Object.keys(result.errors).sort(),
    [`${blankKey.id}.key`, `${blankValue.id}.value`, `${duplicate.id}.key`].sort());
});

test('row IDs are unique', () => {
  assert.notEqual(createNodeLabelRow().id, createNodeLabelRow().id);
});

test('status shape check accepts any well-formed affinity and rejects malformed structures', () => {
  const term = { matchExpressions: [{ key: 'pool', operator: 'In', values: ['benchmark'] }],
    matchFields: [{ key: 'metadata.name', operator: 'NotIn', values: ['node-1'] }] };
  for (const value of [{}, { requiredDuringSchedulingIgnoredDuringExecution: { nodeSelectorTerms: [term] } },
    { preferredDuringSchedulingIgnoredDuringExecution: [{ weight: 50, preference: term }] }]) {
    assert.equal(isNodeAffinity(value), true, JSON.stringify(value));
  }
  const required = value => ({ requiredDuringSchedulingIgnoredDuringExecution: value });
  const expression = item => required({ nodeSelectorTerms: [{ matchExpressions: [item] }] });
  for (const value of [null, [], 'rules', required(null), required({}), required({ nodeSelectorTerms: [null] }),
    expression({ key: 1, operator: 'In' }), expression({ key: 'pool', operator: 'In', values: 'x' }),
    expression({ key: 'pool', operator: 'In', values: [1] }),
    { preferredDuringSchedulingIgnoredDuringExecution: [{ weight: '50', preference: term }] }]) {
    assert.equal(isNodeAffinity(value), false, JSON.stringify(value));
  }
});

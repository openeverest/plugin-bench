import assert from 'node:assert/strict';
import test from 'node:test';
import { initialBenchmarkFormValues, toCreateBenchmarkRunRequest, validateBenchmarkForm } from '../dist/form-test/benchmarkFormValidation.js';

const validValues = { ...initialBenchmarkFormValues, database: 'benchdb' };
const target = { k8sCluster: 'main', namespace: 'dbs', instance: 'pg-1' };

test('accepts valid form values and initializes the dataset by default', () => {
  assert.equal(initialBenchmarkFormValues.initialize, true);
  assert.deepEqual(validateBenchmarkForm(validValues), {});
});

test('database is optional and defaults to the instance database', () => {
  assert.equal(initialBenchmarkFormValues.database, '');
  assert.deepEqual(validateBenchmarkForm({ ...validValues, database: '  ' }), {});
  const request = toCreateBenchmarkRunRequest({ ...validValues, database: '  ' }, target);
  assert.ok(request);
  assert.equal('database' in request, false);
  assert.equal(toCreateBenchmarkRunRequest({ ...validValues, database: ' custom ' }, target).database, 'custom');
});

test('rejects connection strings as the database name', () => {
  assert.equal(validateBenchmarkForm({ ...validValues, database: 'postgres://user:pass@host/db' }).database,
    'Enter a database name, not a connection string.');
  assert.equal(validateBenchmarkForm({ ...validValues, database: 'host=db' }).database,
    'Enter a database name, not a connection string.');
});

test('requires positive whole numbers for all numeric fields', () => {
  for (const invalid of ['', '0', '-1', '1.5', 'abc']) {
    for (const field of ['durationSeconds', 'clients', 'threads', 'scale']) {
      assert.ok(validateBenchmarkForm({ ...validValues, [field]: invalid })[field], `${field} should reject ${invalid}`);
    }
  }
});

test('rejects numeric values above the backend integer limit', () => {
  for (const field of ['durationSeconds', 'clients', 'threads', 'scale']) {
    assert.match(validateBenchmarkForm({ ...validValues, [field]: '2147483648' })[field], /2147483647/);
  }
});

test('requires threads not to exceed clients', () => {
  assert.equal(
    validateBenchmarkForm({ ...validValues, clients: '2', threads: '3' }).threads,
    'Threads cannot exceed clients.'
  );
});

test('reusing tables ignores scale and sends a valid fallback', () => {
  for (const scale of ['', '0', '-1', 'abc', '2147483648', '10']) {
    const values = { ...validValues, initialize: false, scale };
    assert.deepEqual(validateBenchmarkForm(values), {});
    assert.equal(toCreateBenchmarkRunRequest(values, target).scale, 1);
  }
});

test('enabling initialization again requires a valid scale and uses its value', () => {
  const values = { ...validValues, initialize: false, scale: '' };
  assert.ok(toCreateBenchmarkRunRequest(values, target));
  assert.equal(toCreateBenchmarkRunRequest({ ...values, initialize: true }, target), undefined);
  assert.equal(toCreateBenchmarkRunRequest({ ...values, initialize: true, scale: '10' }, target).scale, 10);
});

test('request mapping omits disabled affinity and includes validated enabled rules', () => {
  assert.equal(initialBenchmarkFormValues.nodeAffinity.enabled, false);
  const nodeAffinity = {
    enabled: false, required: [{ matchExpressions: [{ key: 'workload', operator: 'In', values: ['benchmark'] }] }],
    preferred: [{ weight: '50', matchExpressions: [{ key: 'disk', operator: 'In', values: ['ssd', 'nvme'] }] }],
  };
  const values = { ...validValues, nodeAffinity };
  assert.equal('nodeAffinity' in toCreateBenchmarkRunRequest(values, target), false);
  assert.equal('nodeAffinity' in toCreateBenchmarkRunRequest({ ...validValues, nodeAffinity: undefined }, target), false);
  const enabled = { ...values, nodeAffinity: { ...nodeAffinity, enabled: true } };
  const request = toCreateBenchmarkRunRequest(enabled, target);
  assert.deepEqual(request.nodeAffinity, {
    requiredDuringSchedulingIgnoredDuringExecution: { nodeSelectorTerms: nodeAffinity.required },
    preferredDuringSchedulingIgnoredDuringExecution: [{ weight: 50, preference: {
      matchExpressions: nodeAffinity.preferred[0].matchExpressions,
    } }],
  });
  assert.equal(toCreateBenchmarkRunRequest({ ...enabled, clients: '0' }, target), undefined);
});

test('invalid affinity blocks request mapping without polluting scalar field errors', () => {
  const values = { ...validValues, nodeAffinity: {
    enabled: true, required: [], preferred: [{ weight: '101', matchExpressions: [
      { key: 'disk', operator: 'In', values: ['ssd'] },
    ] }],
  } };
  assert.deepEqual(validateBenchmarkForm(values), {});
  assert.equal(toCreateBenchmarkRunRequest(values, target), undefined);
  assert.ok(toCreateBenchmarkRunRequest({ ...values, nodeAffinity: { ...values.nodeAffinity, enabled: false } }, target));
});

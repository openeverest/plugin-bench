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

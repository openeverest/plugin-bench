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

test('resource fields are optional and omitted when empty', () => {
  for (const field of ['cpuRequest', 'cpuLimit', 'memoryRequest', 'memoryLimit']) {
    assert.equal(initialBenchmarkFormValues[field], '');
  }
  const request = toCreateBenchmarkRunRequest(validValues, target);
  assert.ok(request);
  assert.equal('resources' in request, false);
  assert.equal('resources' in toCreateBenchmarkRunRequest({ ...validValues, cpuRequest: '  ' }, target), false);
});

test('partial and full resource overrides use Kubernetes units', () => {
  assert.deepEqual(toCreateBenchmarkRunRequest({
    ...validValues, cpuRequest: '500', memoryLimit: '1024',
  }, target).resources, { cpuRequest: '500m', memoryLimit: '1024Mi' });
  assert.deepEqual(toCreateBenchmarkRunRequest({
    ...validValues, cpuRequest: '500', cpuLimit: '1000', memoryRequest: '128', memoryLimit: '512',
  }, target).resources, {
    cpuRequest: '500m', cpuLimit: '1000m', memoryRequest: '128Mi', memoryLimit: '512Mi',
  });
});

test('resource overrides require positive, safe whole numbers', () => {
  for (const field of ['cpuRequest', 'cpuLimit', 'memoryRequest', 'memoryLimit']) {
    for (const invalid of ['0', '-1', '1.5', 'abc', '1e3', '9007199254740992']) {
      const values = { ...validValues, [field]: invalid };
      assert.match(validateBenchmarkForm(values)[field], /positive whole number/);
      assert.equal(toCreateBenchmarkRunRequest(values, target), undefined);
    }
  }
});

test('entered request cannot exceed entered limit; inherited pairs are checked by backend', () => {
  assert.match(validateBenchmarkForm({ ...validValues, cpuRequest: '1001', cpuLimit: '1000' }).cpuLimit,
    /at least the CPU request/);
  assert.match(validateBenchmarkForm({ ...validValues, memoryRequest: '513', memoryLimit: '512' }).memoryLimit,
    /at least the memory request/);
  assert.deepEqual(validateBenchmarkForm({ ...validValues, cpuRequest: '1001', memoryLimit: '64' }), {});
});

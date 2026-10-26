import assert from 'node:assert/strict';
import test from 'node:test';
import { getBenchmarkRun, BenchmarkStatusError } from '../dist/status-test/benchmarkApi.js';

const running = { id: 'run/1', status: 'running', createdAt: '2026-10-02T10:00:00Z', outputTruncated: false };
const response = body => new Response(JSON.stringify(body));

test('preserves Sysbench effective settings, logs, and optional metrics', async () => {
  for (const status of ['running', 'succeeded', 'failed']) {
    const run = {
      ...running, status, driver: 'sysbench', runnerImage: 'registry/sysbench:v2', database: 'sbtest',
      options: { durationSeconds: 5, threads: 2, tables: 3, tableSize: 500, initialize: false },
      ...(status === 'running' ? {} : { completedAt: running.createdAt, output: 'native sysbench logs', outputTruncated: true }),
      ...(status === 'failed' ? { error: 'benchmark execution failed' } : {}),
    };
    for (const metrics of [undefined, [{ name: 'tps', unit: 'tx/s', value: 1234.5 }, { name: 'latency_avg', unit: 'ms', value: 3.2 }]]) {
      const snapshot = { ...run, ...(metrics ? { metrics } : {}) };
      assert.deepEqual(await getBenchmarkRun(async () => response(snapshot), run.id), snapshot);
    }
  }
});

test('GET encodes the ID and accepts all statuses and optional diagnostics', async () => {
  for (const status of ['running', 'succeeded', 'failed']) {
    const run = { ...running, status, ...(status === 'running' ? {} : {
      completedAt: '2026-10-02T10:01:00Z', output: '<output>', jobName: 'job-1',
      outputTruncated: true, ...(status === 'failed' ? { error: 'cleanup failed' } : {}),
    }) };
    assert.deepEqual(await getBenchmarkRun(async (path, init) => {
      assert.equal(path, '/api/runs/run%2F1');
      assert.equal(init.method, 'GET');
      assert.ok(init.signal instanceof AbortSignal);
      return response(run);
    }, running.id), run);
  }
});

test('status accepts optional resolved resource snapshots without changing them', async () => {
  const resources = { cpuRequest: '500m', cpuLimit: '1', memoryRequest: '128Mi', memoryLimit: '1Gi' };
  for (const run of [running, { ...running, resources }, { ...running, resources: {} }]) {
    assert.deepEqual(await getBenchmarkRun(async () => response(run), running.id), run);
  }
});

test('GET preserves driver snapshots and generic metrics for every run status', async () => {
  for (const status of ['running', 'succeeded', 'failed']) {
    const run = {
      ...running, status,
      driver: 'another-tool', runnerImage: 'ghcr.io/example/another-tool:2.0',
      options: { duration: 60, nested: { mode: 'read' }, enabled: false, labels: ['a'], optional: null },
      metrics: [{ name: 'custom_metric', unit: 'items/s', value: 0 },
        { name: 'latency_avg', unit: 'ms', value: 3.2 }],
      ...(status === 'running' ? {} : { completedAt: running.createdAt, output: 'tool output', outputTruncated: true }),
      ...(status === 'failed' ? { error: 'cleanup failed' } : {}),
    };
    assert.deepEqual(await getBenchmarkRun(async () => response(run), running.id), run);
  }
});

test('legacy responses omit unavailable metadata, while empty options remain a valid snapshot', async () => {
  const legacy = await getBenchmarkRun(async () => response(running), running.id);
  assert.deepEqual(legacy, running);
  for (const key of ['driver', 'runnerImage', 'options', 'metrics']) assert.equal(key in legacy, false);
  assert.deepEqual(await getBenchmarkRun(async () => response({ ...running, options: {} }), running.id),
    { ...running, options: {} });
});

test('malformed optional metadata is omitted independently and cannot hide terminal diagnostics', async () => {
  const terminal = { ...running, status: 'failed', completedAt: running.createdAt,
    output: 'saved logs', outputTruncated: true, error: 'execution failed' };
  const valid = { driver: 'pgbench', runnerImage: 'runner:v1', options: { clients: 4 },
    metrics: [{ name: 'tps', unit: 'tx/s', value: 1234.5 }] };
  for (const [field, invalidValues] of [
    ['driver', [null, 42, false, [], {}, '', '  ']],
    ['runnerImage', [null, 42, false, [], {}, '', '  ']],
    ['options', [null, [], 'options', 42, false]],
  ]) {
    for (const invalid of invalidValues) {
      const expected = { ...terminal, ...valid };
      delete expected[field];
      assert.deepEqual(await getBenchmarkRun(async () => response({ ...terminal, ...valid, [field]: invalid }), running.id),
        expected);
    }
  }
});

test('malformed metric sets become unavailable without losing logs, status, or valid metadata', async () => {
  const run = { ...running, status: 'succeeded', completedAt: running.createdAt,
    driver: 'pgbench', options: { clients: 4 }, output: 'raw logs', outputTruncated: true };
  const metric = { name: 'tps', unit: 'tx/s', value: 1234.5 };
  for (const metrics of [null, {}, 'metrics', [], [null], [[]], ['metric'],
    [{ ...metric, name: '' }], [{ ...metric, name: '  ' }], [{ ...metric, name: 42 }],
    [{ ...metric, unit: '' }], [{ ...metric, unit: '  ' }], [{ ...metric, unit: 42 }],
    [{ name: 'tps', unit: 'tx/s' }], [{ ...metric, value: null }], [{ ...metric, value: '1234.5' }],
    [{ ...metric, value: false }], [metric, { ...metric }], [metric, { ...metric, unit: null }],
  ]) {
    assert.deepEqual(await getBenchmarkRun(async () => response({ ...run, metrics }), running.id), run);
  }
  for (const number of ['1e400', '-1e400']) {
    const json = JSON.stringify({ ...run, metrics: [{ ...metric, value: 'overflow' }] })
      .replace('"overflow"', number);
    assert.deepEqual(await getBenchmarkRun(async () => new Response(json), running.id), run);
  }
});

test('metric identity includes both name and unit and preserves numeric zero', async () => {
  const metrics = [
    { name: 'throughput', unit: 'tx/s', value: 0 },
    { name: 'throughput', unit: 'bytes/s', value: 50 },
    { name: 'delta', unit: 'ms', value: -1.5 },
  ];
  assert.deepEqual(await getBenchmarkRun(async () => response({ ...running, metrics }), running.id),
    { ...running, metrics });
});

test('invalid JSON, shapes, IDs and terminal fields are rejected', async () => {
  for (const body of [null, {}, { ...running, id: 'other' }, { ...running, status: 'pending' },
    { ...running, createdAt: 'invalid' }, { ...running, outputTruncated: 'false' },
    { ...running, output: 123 }, { ...running, completedAt: null },
    { ...running, resources: null }, { ...running, resources: [] },
    { ...running, resources: { cpuRequest: 500 } }, { ...running, resources: { memoryLimit: null } },
    { ...running, status: 'succeeded' },
    { ...running, status: 'failed', completedAt: running.createdAt }]) {
    await assert.rejects(getBenchmarkRun(async () => response(body), running.id),
      error => error instanceof BenchmarkStatusError && !error.retryable);
  }
  await assert.rejects(getBenchmarkRun(async () => new Response('not JSON'), running.id), BenchmarkStatusError);
  await assert.rejects(getBenchmarkRun(async () => new Response(null, { status: 204 }), running.id), BenchmarkStatusError);
});

test('HTTP errors preserve messages and classify retryable failures', async () => {
  for (const status of [400, 401, 403, 404, 429, 500, 503]) {
    await assert.rejects(getBenchmarkRun(async () => new Response('{"error":"unavailable"}', { status }), running.id),
      error => error instanceof BenchmarkStatusError && error.status === status &&
        error.message === 'unavailable' && error.retryable === (status === 429 || status >= 500));
  }
  await assert.rejects(getBenchmarkRun(async () => new Response('bad gateway', { status: 502 }), running.id),
    error => error.message === 'The benchmark service returned HTTP 502.');
  await assert.rejects(getBenchmarkRun(async () => { throw new TypeError('network'); }, running.id),
    error => error instanceof BenchmarkStatusError && error.retryable);
});

test('timeout bounds both fetch and body reading even if abort is ignored', async () => {
  for (const stallBody of [false, true]) {
    let signal;
    await assert.rejects(getBenchmarkRun((_path, init) => {
      signal = init.signal;
      return stallBody ? Promise.resolve({ ok: true, status: 200, json: () => new Promise(() => {}) })
        : new Promise(() => {});
    }, running.id, { timeoutMs: 10 }),
    error => error instanceof BenchmarkStatusError && error.retryable && /timed out/.test(error.message));
    assert.equal(signal.aborted, true);
  }
});

test('caller cancellation is identifiable and pre-abort never sends a GET', async () => {
  const controller = new AbortController();
  let finish;
  const pending = getBenchmarkRun(() => new Promise(resolve => { finish = resolve; }), running.id,
    { signal: controller.signal });
  controller.abort();
  await assert.rejects(pending, error => error.name === 'AbortError');
  finish(response(running));
  let calls = 0;
  await assert.rejects(getBenchmarkRun(async () => { calls++; return response(running); }, running.id,
    { signal: controller.signal }), error => error.name === 'AbortError');
  assert.equal(calls, 0);
});

test('successful retrieval removes the caller cancellation listener', async () => {
  const controller = new AbortController();
  let signal;
  await getBenchmarkRun(async (_path, init) => { signal = init.signal; return response(running); }, running.id,
    { signal: controller.signal });
  controller.abort();
  assert.equal(signal.aborted, false);
});

test('GET preserves saved affinity for every status and accepts older responses without it', async () => {
  const term = { matchExpressions: [{ key: 'workload', operator: 'In', values: ['benchmark'] }],
    matchFields: [{ key: 'metadata.name', operator: 'NotIn', values: ['node-2'] }] };
  const custom = {
    requiredDuringSchedulingIgnoredDuringExecution: { nodeSelectorTerms: [term] },
    preferredDuringSchedulingIgnoredDuringExecution: [{ weight: 75, preference: term }],
  };
  for (const status of ['running', 'succeeded', 'failed']) {
    for (const nodeAffinity of [undefined, {}, custom]) {
      const run = { ...running, status,
        ...(nodeAffinity === undefined ? {} : { nodeAffinity }),
        ...(status === 'running' ? {} : { completedAt: running.createdAt }),
        ...(status === 'failed' ? { error: 'benchmark execution failed' } : {}),
      };
      assert.deepEqual(await getBenchmarkRun(async () => response(run), running.id), run);
    }
  }
});

test('GET rejects malformed affinity instead of silently dropping scheduling rules', async () => {
  for (const nodeAffinity of [null, [], 'rules', { requiredDuringSchedulingIgnoredDuringExecution: {} },
    { requiredDuringSchedulingIgnoredDuringExecution: { nodeSelectorTerms: [null] } },
    { preferredDuringSchedulingIgnoredDuringExecution: [{ weight: '50', preference: {} }] },
    { preferredDuringSchedulingIgnoredDuringExecution: [{ weight: 50, preference: {
      matchExpressions: [{ key: 'disk', operator: 'In', values: [42] }],
    } }] },
  ]) {
    await assert.rejects(getBenchmarkRun(async () => response({ ...running, nodeAffinity }), running.id),
      error => error instanceof BenchmarkStatusError && !error.retryable && /invalid response/.test(error.message));
  }
});

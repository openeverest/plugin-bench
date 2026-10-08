import assert from 'node:assert/strict';
import test from 'node:test';
import { getBenchmarkRun, BenchmarkStatusError } from '../dist/status-test/benchmarkApi.js';

const running = { id: 'run/1', status: 'running', createdAt: '2026-10-02T10:00:00Z', outputTruncated: false };
const response = body => new Response(JSON.stringify(body));

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

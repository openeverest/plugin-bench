import assert from 'node:assert/strict';
import test from 'node:test';
import { BenchmarkApiError, createBenchmarkRun } from '../dist/submission-test/benchmarkApi.js';

const request = {
  target: { k8sCluster: 'main', namespace: 'default', instance: 'postgres-a' },
  database: 'bench', durationSeconds: 30, clients: 1, threads: 1, scale: 1, initialize: false,
};
const unknownOutcome = error => error instanceof BenchmarkApiError && error.outcomeUnknown;

test('lost response reports an uncertain outcome without retrying the POST', async () => {
  let calls = 0;
  await assert.rejects(createBenchmarkRun(async () => {
    calls++;
    throw new TypeError('Connection dropped after acceptance');
  }, request), error => {
    assert.ok(unknownOutcome(error));
    assert.match(error.message, /may already be running/);
    assert.doesNotMatch(error.message, /Please try again/);
    return true;
  });
  assert.equal(calls, 1);
});

test('deadline bounds stalled fetch even when the transport ignores abort', async () => {
  let signal;
  await assert.rejects(createBenchmarkRun((_path, init) => {
    signal = init.signal;
    return new Promise(() => {});
  }, request, { timeoutMs: 10 }), error => unknownOutcome(error) && /timed out/.test(error.message));
  assert.equal(signal.aborted, true);
});

test('deadline also covers reading the response body', async () => {
  let signal;
  await assert.rejects(createBenchmarkRun(async (_path, init) => {
    signal = init.signal;
    return { ok: true, status: 202, json: () => new Promise(() => {}) };
  }, request, { timeoutMs: 10 }), unknownOutcome);
  assert.equal(signal.aborted, true);
});

test('caller cancellation settles the request and ignores late success', async () => {
  const controller = new AbortController();
  let signal;
  let finish;
  const pending = createBenchmarkRun((_path, init) => {
    signal = init.signal;
    return new Promise(resolve => { finish = resolve; });
  }, request, { signal: controller.signal });
  controller.abort();
  await assert.rejects(pending, unknownOutcome);
  assert.equal(signal.aborted, true);
  finish(new Response(JSON.stringify({ id: 'late-run', status: 'running' }), { status: 202 }));
  await assert.rejects(pending, unknownOutcome);
});

test('an already cancelled caller never sends a POST', async () => {
  const controller = new AbortController();
  controller.abort();
  let calls = 0;
  await assert.rejects(createBenchmarkRun(async () => { calls++; }, request, {
    signal: controller.signal,
  }), unknownOutcome);
  assert.equal(calls, 0);
});

test('unreadable or invalid acceptance and gateway errors remain uncertain', async () => {
  for (const [body, status] of [['not JSON', 202], ['{}', 202], ['{"error":"gateway unavailable"}', 502]]) {
    await assert.rejects(createBenchmarkRun(async () => new Response(body, { status }), request),
      error => unknownOutcome(error) && error.status === status);
  }
});

test('explicit rejection retains its message and is distinguishable from uncertainty', async () => {
  await assert.rejects(createBenchmarkRun(async () => new Response('{"error":"capacity full"}', {
    status: 429,
  }), request), error => error instanceof BenchmarkApiError && !error.outcomeUnknown &&
    error.status === 429 && error.message === 'capacity full');
});

test('accepted response succeeds and disconnects the caller abort listener', async () => {
  const controller = new AbortController();
  let signal;
  const result = await createBenchmarkRun(async (_path, init) => {
    signal = init.signal;
    return new Response('{"id":"run-1","status":"running"}', { status: 202 });
  }, request, { signal: controller.signal });
  assert.deepEqual(result, { id: 'run-1', status: 'running' });
  controller.abort();
  assert.equal(signal.aborted, false);
});

test('POST forwards native affinity without changing the acceptance contract', async () => {
  const nodeAffinity = {
    requiredDuringSchedulingIgnoredDuringExecution: { nodeSelectorTerms: [{
      matchExpressions: [{ key: 'workload', operator: 'In', values: ['benchmark'] }],
    }] },
    preferredDuringSchedulingIgnoredDuringExecution: [{ weight: 50, preference: {
      matchExpressions: [{ key: 'disk', operator: 'In', values: ['ssd'] }],
    } }],
  };
  for (const payload of [request, { ...request, nodeAffinity }]) {
    const before = structuredClone(payload);
    assert.deepEqual(await createBenchmarkRun(async (path, init) => {
      assert.equal(path, '/api/runs');
      assert.equal(init.method, 'POST');
      assert.deepEqual(JSON.parse(init.body), before);
      return new Response('{"id":"run-1","status":"running"}', { status: 202 });
    }, payload), { id: 'run-1', status: 'running' });
    assert.deepEqual(payload, before);
  }
});

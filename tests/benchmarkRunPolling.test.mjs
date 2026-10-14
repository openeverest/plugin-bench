import assert from 'node:assert/strict';
import test from 'node:test';
import { startBenchmarkRunPolling } from '../dist/polling-test/benchmarkRunPolling.js';
import { BenchmarkStatusError } from '../dist/polling-test/benchmarkApi.js';

const flush = async () => { await Promise.resolve(); await Promise.resolve(); };
const snapshot = status => ({ id: 'run-1', status, createdAt: '2026-10-02T00:00:00Z', outputTruncated: false });

test('independent sessions retain both results and stopping one leaves the other active', async () => {
  let firstFinish, secondFinish;
  const first = harness(() => new Promise(resolve => { firstFinish = resolve; }));
  const second = harness(() => new Promise(resolve => { secondFinish = resolve; }));
  const stopFirst = first.start();
  second.start();
  firstFinish(snapshot('succeeded'));
  secondFinish(snapshot('running'));
  await flush();
  stopFirst();
  assert.equal(first.runs[0].status, 'succeeded');
  assert.equal(second.timers.size, 1);
  second.tick();
  secondFinish(snapshot('failed'));
  await flush();
  assert.equal(first.runs[0].status, 'succeeded');
  assert.equal(second.runs.at(-1).status, 'failed');
  assert.equal(second.timers.size, 0);
});

function harness(fetchRun) {
  const timers = new Map();
  const runs = [], errors = [], ids = [], signals = [];
  let next = 0;
  const start = () => startBenchmarkRunPolling(() => {}, 'run-1', {
    fetchRun: (...args) => { ids.push(args[1]); signals.push(args[2].signal); return fetchRun(...args); },
    onRun: run => runs.push(run), onError: error => errors.push(error),
    schedule: (callback, delay) => { const id = ++next; timers.set(id, { callback, delay }); return id; },
    clearScheduled: id => timers.delete(id),
  });
  const tick = () => {
    assert.equal(timers.size, 1);
    const [id, timer] = timers.entries().next().value;
    timers.delete(id);
    timer.callback();
    return timer.delay;
  };
  return { start, tick, timers, runs, errors, ids, signals };
}

test('requests never overlap and terminal success/failure stops polling', async () => {
  for (const status of ['succeeded', 'failed']) {
    let finish;
    const h = harness(() => new Promise(resolve => { finish = resolve; }));
    h.start();
    assert.equal(h.ids.length, 1);
    assert.equal(h.timers.size, 0);
    finish(snapshot('running'));
    await flush();
    assert.equal(h.tick(), 2000);
    finish(snapshot(status));
    await flush();
    assert.deepEqual(h.runs.map(run => run.status), ['running', status]);
    assert.equal(h.timers.size, 0);
  }
});

test('polling retains each run resource snapshot', async () => {
  const resources = { cpuRequest: '500m', memoryLimit: '1Gi' };
  const h = harness(async () => ({ ...snapshot('succeeded'), resources }));
  h.start();
  await flush();
  assert.deepEqual(h.runs[0].resources, resources);
});

test('retry exhaustion pauses after three retries; restarting uses the same ID', async () => {
  const h = harness(async () => { throw new BenchmarkStatusError('offline', undefined, true); });
  h.start();
  await flush();
  for (const delay of [2000, 4000, 8000]) {
    assert.equal(h.tick(), delay);
    await flush();
  }
  assert.equal(h.errors.at(-1).paused, true);
  assert.equal(h.timers.size, 0);
  assert.equal(h.ids.length, 4);
  h.start();
  await flush();
  assert.equal(h.errors.at(-1).retryInMs, 2000);
  assert.ok(h.ids.every(id => id === 'run-1'));
});

test('successful recovery resets retry delays', async () => {
  let call = 0;
  const h = harness(async () => {
    if (++call === 2) return snapshot('running');
    throw new BenchmarkStatusError('offline', 503, true);
  });
  h.start(); await flush();
  h.tick(); await flush();
  assert.equal(h.runs.length, 1);
  h.tick(); await flush();
  assert.equal(h.errors.at(-1).retryInMs, 2000);
});

test('permanent retrieval failures pause without changing run status', async () => {
  for (const status of [401, 403, 404, 400, 200]) {
    const h = harness(async () => { throw new BenchmarkStatusError('unavailable', status); });
    h.start(); await flush();
    assert.equal(h.errors[0].paused, true);
    assert.equal(h.runs.length, 0);
    assert.equal(h.timers.size, 0);
  }
});

test('cleanup aborts in-flight GET and suppresses late responses and failures', async () => {
  for (const rejectLate of [false, true]) {
    let resolve, reject;
    const h = harness(() => new Promise((res, rej) => { resolve = res; reject = rej; }));
    const stop = h.start();
    stop(); stop();
    assert.equal(h.signals[0].aborted, true);
    if (rejectLate) reject(new BenchmarkStatusError('offline', undefined, true));
    else resolve(snapshot('succeeded'));
    await flush();
    assert.equal(h.runs.length + h.errors.length + h.timers.size, 0);
  }
});

test('cleanup clears a scheduled poll; intentional cancellation is silent', async () => {
  const h = harness(async () => snapshot('running'));
  const stop = h.start(); await flush();
  stop();
  assert.equal(h.timers.size, 0);
  const cancelled = harness(async () => { throw new DOMException('cancelled', 'AbortError'); });
  cancelled.start(); await flush();
  assert.equal(cancelled.errors.length + cancelled.timers.size, 0);
});

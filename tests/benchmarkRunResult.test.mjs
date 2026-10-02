import assert from 'node:assert/strict';
import test from 'node:test';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { BenchmarkRunResult } from '../dist/result-test/BenchmarkRunResult.js';

const base = { id: 'run-1', status: 'succeeded', createdAt: '2026-10-02T00:00:00Z',
  completedAt: '2026-10-02T00:01:00Z', outputTruncated: false };
const render = (run, feedback = null) => renderToStaticMarkup(React.createElement(BenchmarkRunResult,
  { react: React, id: 'run-1', run, feedback, onRetry: () => {} }));

test('terminal output and errors are escaped and output is outside the live region', () => {
  const html = render({ ...base, status: 'failed', error: '<script>error</script>',
    output: '<script>output</script>\nTPS: 123', outputTruncated: true });
  assert.match(html, /Status: Failed/);
  assert.match(html, /&lt;script&gt;output&lt;\/script&gt;/);
  assert.match(html, /&lt;script&gt;error&lt;\/script&gt;/);
  assert.doesNotMatch(html, /<script>/);
  assert.match(html, /Output was truncated by the backend/);
  assert.match(html, /Completed/);
  assert.match(html, /<\/p>.*<pre/s);
});

test('empty completed output is explicit and running output is not displayed', () => {
  assert.match(render(base), /No output was captured/);
  const html = render({ ...base, status: 'running', completedAt: undefined });
  assert.match(html, /Status: Running/);
  assert.doesNotMatch(html, /No output was captured|<pre/);
});

test('retrieval failure preserves last known run status and offers retry', () => {
  const feedback = { paused: true, error: { message: 'Access denied' } };
  assert.match(render({ ...base, status: 'running' }, feedback), /Last known status: Running/);
  assert.match(render(null, feedback), /Status unavailable/);
  assert.match(render(null, feedback), /Retry status check/);
  assert.match(render(null), /Checking status/);
});

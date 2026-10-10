import assert from 'node:assert/strict';
import test from 'node:test';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { BenchmarkRunResult } from '../dist/result-test/BenchmarkRunResult.js';

const base = { id: 'run-1', status: 'succeeded', createdAt: '2026-10-02T00:00:00Z',
  completedAt: '2026-10-02T00:01:00Z', outputTruncated: false };
const render = (run, feedback = null) => renderToStaticMarkup(React.createElement(BenchmarkRunResult,
  { id: 'run-1', run, feedback, onRetry: () => {} }));

test('terminal output and errors are escaped and output follows the alerts', () => {
  const html = render({ ...base, status: 'failed', error: '<script>error</script>',
    output: '<script>output</script>\nTPS: 123', outputTruncated: true });
  assert.match(html, /Status: Failed/);
  assert.match(html, /&lt;script&gt;output&lt;\/script&gt;/);
  assert.match(html, /&lt;script&gt;error&lt;\/script&gt;/);
  assert.doesNotMatch(html, /<script>/);
  assert.match(html, /Output was truncated by the backend/);
  assert.match(html, /Completed/);
  assert.ok(html.indexOf('&lt;script&gt;error') < html.indexOf('<pre'));
  assert.doesNotMatch(html, /<pre[^>]*(role|aria-live)=/);
});

test('shows the database the run used', () => {
  assert.match(render({ ...base, database: 'app' }), /Database<\/dt><dd[^>]*>app<\/dd>/);
  assert.doesNotMatch(render(base), /Database/);
});

test('empty completed output is explicit and running output is not displayed', () => {
  assert.match(render(base), /No output was captured/);
  const html = render({ ...base, status: 'running', completedAt: undefined });
  assert.match(html, /Status: Running/);
  assert.doesNotMatch(html, /No output was captured|<pre/);
});

test('dismiss is offered only for completed runs', () => {
  const card = run => renderToStaticMarkup(React.createElement(BenchmarkRunResult,
    { id: 'run-1', run, feedback: null, onRetry: () => {}, onDismiss: () => {} }));
  assert.match(card(base), /Dismiss completed run run-1/);
  assert.doesNotMatch(card({ ...base, status: 'running' }), /Dismiss/);
});

test('retrieval failure preserves last known run status and offers retry', () => {
  const feedback = { paused: true, error: { message: 'Access denied' } };
  assert.match(render({ ...base, status: 'running' }, feedback), /Last known status: Running/);
  assert.match(render(null, feedback), /Status unavailable/);
  assert.match(render(null, feedback), /Retry status check/);
  assert.match(render(null), /Checking status/);
});

test('saved affinity displays required alternatives, AND conditions, weights, and native fields for all statuses', () => {
  const nodeAffinity = {
    requiredDuringSchedulingIgnoredDuringExecution: { nodeSelectorTerms: [
      { matchExpressions: [
        { key: 'workload', operator: 'In', values: ['benchmark'] },
        { key: 'generation', operator: 'Gt', values: ['3'] },
      ] },
      { matchFields: [{ key: 'metadata.name', operator: 'NotIn', values: ['node-2'] }] },
    ] },
    preferredDuringSchedulingIgnoredDuringExecution: [{ weight: 75, preference: {
      matchExpressions: [{ key: 'disk', operator: 'In', values: ['ssd', 'nvme'] }],
      matchFields: [{ key: 'metadata.name', operator: 'In', values: ['node-1'] }],
    } }],
  };
  for (const status of ['running', 'succeeded', 'failed']) {
    const html = render({ ...base, status, nodeAffinity, ...(status === 'failed' ? { error: 'failed' } : {}) });
    assert.match(html, /Configured node affinity/);
    assert.match(html, /Required group 1 \(AND\)/);
    assert.match(html, /OR — Required group 2/);
    assert.match(html, /Label: workload In/);
    assert.match(html, /Label: generation Gt/);
    assert.match(html, /Field: metadata.name NotIn/);
    assert.match(html, /Preferred group 1 — weight 75/);
    assert.match(html, /Field: metadata.name In/);
    assert.match(html, /ssd/);
    assert.match(html, /nvme/);
    assert.match(html, /not the node ultimately selected/);
  }
});

test('empty affinity is explicit; older responses without affinity do not imply no rules', () => {
  assert.match(render({ ...base, nodeAffinity: {} }), /No node affinity configured/);
  assert.doesNotMatch(render(base), /Configured node affinity|No node affinity configured/);
  assert.doesNotMatch(render(null), /Configured node affinity/);
});

test('affinity strings are escaped as text, including field values and empty label values', () => {
  const html = render({ ...base, nodeAffinity: {
    requiredDuringSchedulingIgnoredDuringExecution: { nodeSelectorTerms: [{ matchExpressions: [
      { key: '<script>key</script>', operator: 'In', values: ['<img src=x onerror=alert(1)>', ''] },
    ], matchFields: [{ key: 'metadata.name', operator: 'In', values: ['<script>node</script>'] }] }] },
  } });
  assert.doesNotMatch(html, /<script>|<img/);
  assert.match(html, /&lt;script&gt;key/);
  assert.match(html, /&lt;script&gt;node/);
  assert.match(html, /&quot;&quot;/);
});

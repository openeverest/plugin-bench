import { afterEach, describe, expect, it, vi } from 'vitest';
import { getBenchmarkInstances } from '../src/benchmarkInstancesApi';

const instance = { k8sCluster: 'main', namespace: 'default', name: 'db', provider: 'percona-server-mysql' };
const options = { k8sCluster: 'main' };
const respond = (body: unknown, status = 200) => vi.fn(async () => new Response(JSON.stringify(body), { status }));
afterEach(() => vi.useRealTimers());

describe('instance discovery client', () => {
  it('encodes the cluster, forwards a signal, and returns only metadata', async () => {
    const cluster = 'team/a & b';
    const fetch = respond({ instances: [{ ...instance, k8sCluster: cluster, extra: 'ignored' }] });
    expect(await getBenchmarkInstances(fetch, { k8sCluster: cluster })).toEqual([{ ...instance, k8sCluster: cluster }]);
    expect(fetch).toHaveBeenCalledWith('/api/instances?k8sCluster=team%2Fa+%26+b', {
      method: 'GET', signal: expect.any(AbortSignal),
    });
  });

  it('accepts an empty list', async () => {
    expect(await getBenchmarkInstances(respond({ instances: [] }), options)).toEqual([]);
  });

  it.each([null, {}, { instances: null }, { instances: {} }, { instances: [null] },
    ...Object.keys(instance).flatMap(key => [
      { instances: [{ ...instance, [key]: undefined }] },
      { instances: [{ ...instance, [key]: ' ' }] },
      { instances: [{ ...instance, [key]: 12 }] },
    ]), { instances: [{ ...instance, k8sCluster: 'other' }] },
  ])('rejects malformed or wrong-cluster metadata: %j', async body => {
    await expect(getBenchmarkInstances(respond(body), options)).rejects.toThrow('invalid instance list');
  });

  it.each([401, 403, 404, 502, 504])('reports HTTP %s without exposing the response body', async status => {
    await expect(getBenchmarkInstances(respond({ error: 'private upstream details' }, status), options))
      .rejects.toThrow(`Could not discover database instances (HTTP ${status}).`);
  });

  it('handles invalid JSON and network errors without exposing their details', async () => {
    for (const fetch of [
      vi.fn(async () => new Response('private invalid JSON')),
      vi.fn(async () => { throw new Error('private network details'); }),
    ]) {
      await expect(getBenchmarkInstances(fetch, options)).rejects.toThrow('Could not discover database instances.');
    }
  });

  it('does not send a request for an empty cluster or an already aborted signal', async () => {
    const fetch = respond({ instances: [] });
    await expect(getBenchmarkInstances(fetch, { k8sCluster: ' ' })).rejects.toThrow('cluster is required');
    const controller = new AbortController();
    controller.abort();
    await expect(getBenchmarkInstances(fetch, { ...options, signal: controller.signal }))
      .rejects.toMatchObject({ name: 'AbortError' });
    expect(fetch).not.toHaveBeenCalled();
  });

  it('cancels even when the transport ignores abort', async () => {
    const controller = new AbortController();
    const fetch = vi.fn((_path: string, init?: RequestInit) => {
      expect(init?.signal?.aborted).toBe(false);
      return new Promise<Response>(() => {});
    });
    const request = getBenchmarkInstances(fetch, { ...options, signal: controller.signal });
    const assertion = expect(request).rejects.toMatchObject({ name: 'AbortError' });
    controller.abort();
    await assertion;
    expect(fetch.mock.calls[0][1]?.signal?.aborted).toBe(true);
  });

  it.each(['fetch', 'body'])('bounds %s processing with the request timeout', async stage => {
    vi.useFakeTimers();
    const fetch = vi.fn(async () => stage === 'fetch'
      ? new Promise<Response>(() => {})
      : { status: 200, json: () => new Promise(() => {}) } as Response);
    const request = getBenchmarkInstances(fetch, { ...options, timeoutMs: 50 });
    const assertion = expect(request).rejects.toThrow('timed out');
    await vi.advanceTimersByTimeAsync(50);
    await assertion;
    expect(vi.getTimerCount()).toBe(0);
  });
});

import * as React from 'react';
import type { ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import type { Extension, PluginApi } from '@openeverest/plugin-sdk';
import register from '../src/main';
import { benchmarkProviderNames } from '../src/drivers';

vi.mock('../src/PluginRoot', () => ({
  PluginRoot: ({ nonce, children }: { nonce?: string; children: ReactNode }) => (
    <div data-testid="plugin-root" data-nonce={nonce}>{children}</div>
  ),
}));

afterEach(cleanup);

function setup() {
  const extensions: Extension[] = [];
  const requests: Array<{ context: unknown; path: string; init?: RequestInit }> = [];
  const api: PluginApi = {
    React,
    basePath: '/v1/clusters/main/plugins/plugin-bench',
    cssNonce: 'test-nonce', hostVersion: 'dev', uiContractVersion: '18',
    registerExtension: extension => { extensions.push(extension); },
    async fetch(path, init) {
      requests.push({ context: this, path, init });
      return new Response(JSON.stringify({ error: 'selected driver is disabled' }), { status: 400 });
    },
  };
  register(api);
  return { api, extensions, requests };
}

describe('benchmark plugin registration', () => {
  it('exposes the benchmark tab for the providers in the driver registry', () => {
    const { extensions } = setup();
    expect(extensions.map(extension => extension.type)).toEqual(['sidebarItem', 'route', 'clusterDetailTab']);
    const tab = extensions.find(extension => extension.type === 'clusterDetailTab');
    expect(tab).toMatchObject({
      path: 'performance-benchmark', label: 'Performance Benchmark', providers: benchmarkProviderNames,
    });
    expect(tab?.providers).toContain('percona-server-mysql');
  });

  it('forwards MySQL instance context, CSP nonce, and the bound authenticated fetch to the tab', async () => {
    const { api, extensions, requests } = setup();
    const tab = extensions.find(extension => extension.type === 'clusterDetailTab');
    if (!tab) throw new Error('Benchmark tab was not registered');
    const Component = tab.component;
    render(<Component cluster={{ clusterName: 'secondary', spec: { providerRef: { name: 'percona-server-mysql' } } }}
      namespace="dbs" instanceName="mysql-1" />);
    expect(screen.getByTestId('plugin-root').getAttribute('data-nonce')).toBe('test-nonce');
    expect(screen.getByRole('combobox', { name: 'Benchmark driver' }).textContent).toBe('Sysbench');
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Start benchmark' })); });
    expect(requests).toHaveLength(1);
    expect(requests[0].context).toBe(api);
    expect(requests[0].path).toBe('/api/runs');
    expect(requests[0].init?.method).toBe('POST');
    expect(JSON.parse(String(requests[0].init?.body))).toMatchObject({
      target: { k8sCluster: 'secondary', namespace: 'dbs', instance: 'mysql-1' }, driver: 'sysbench',
    });
  });
});

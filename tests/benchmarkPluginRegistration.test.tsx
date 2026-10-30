import * as React from 'react';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import type { ReactNode } from 'react';
import { parse as parseYaml } from 'yaml';
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
      if (path === '/api/status') {
        return new Response(JSON.stringify({ drivers: ['pgbench', 'sysbench'] }), { status: 200 });
      }
      return new Response(JSON.stringify({ error: 'selected driver is disabled' }), { status: 400 });
    },
  };
  register(api);
  return { api, extensions, requests };
}

describe('benchmark plugin registration', () => {
  it('keeps the Helm chart benchmark-tab providers in sync with the driver registry', () => {
    const values: { plugin: { extensionPoints: Array<{ type: string; path?: string; providers?: string[] }> } } =
      parseYaml(readFileSync(resolve(process.cwd(), 'charts/plugin-bench/values.yaml'), 'utf8'));
    const tabs = values.plugin.extensionPoints.filter(extension =>
      extension.type === 'clusterDetailTab' && extension.path === 'performance-benchmark');
    expect(tabs).toHaveLength(1);
    expect(tabs[0].providers).toEqual(expect.any(Array));
    expect([...(tabs[0].providers ?? [])].sort()).toEqual([...benchmarkProviderNames].sort());
  });

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
    await act(async () => {
      render(<Component cluster={{ clusterName: 'secondary', spec: { providerRef: { name: 'percona-server-mysql' } } }}
        namespace="dbs" instanceName="mysql-1" />);
    });
    expect(screen.getByTestId('plugin-root').getAttribute('data-nonce')).toBe('test-nonce');
    expect(screen.getByRole('combobox', { name: 'Benchmark driver' }).textContent).toBe('Sysbench');
    fireEvent.change(screen.getByLabelText('Database'), { target: { value: 'sbtest' } });
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Start benchmark' })); });
    expect(requests).toHaveLength(2);
    expect(requests.every(request => request.context === api)).toBe(true);
    expect(requests[0].path).toBe('/api/status');
    expect(requests[1].path).toBe('/api/runs');
    expect(requests[1].init?.method).toBe('POST');
    expect(JSON.parse(String(requests[1].init?.body))).toMatchObject({
      target: { k8sCluster: 'secondary', namespace: 'dbs', instance: 'mysql-1' },
      driver: 'sysbench', database: 'sbtest',
    });
  });
});

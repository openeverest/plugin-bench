import { describe, expect, it } from 'vitest';
import {
  benchmarkDriverIds, benchmarkProviderNames, createDriverDraft, describeRunOptions,
  getBenchmarkDriversForProvider, prepareDriver,
} from '../src/drivers';
import { createInitialBenchmarkFormValues, toCreateBenchmarkRunRequest } from '../src/benchmarkFormValidation';

describe('benchmark driver registry', () => {
  it('registers both drivers and their provider metadata', () => {
    expect(benchmarkDriverIds).toEqual(['pgbench', 'sysbench']);
    expect(benchmarkProviderNames).toEqual([
      'provider-cloudnative-pg', 'provider-percona-postgresql', 'percona-server-mysql',
    ]);
    expect(getBenchmarkDriversForProvider('provider-cloudnative-pg')).toEqual(['pgbench']);
    expect(getBenchmarkDriversForProvider('provider-percona-postgresql')).toEqual(['pgbench']);
    expect(getBenchmarkDriversForProvider('percona-server-mysql')).toEqual(['sysbench']);
    for (const provider of [undefined, '', 'unknown', 'provider-mariadb', 'provider-percona-xtradb-cluster']) {
      expect(getBenchmarkDriversForProvider(provider)).toEqual([]);
    }
  });

  it('creates independent Sysbench options and placement drafts', () => {
    const first = createInitialBenchmarkFormValues('sysbench');
    const second = createInitialBenchmarkFormValues('sysbench');
    const draft = createDriverDraft('sysbench');
    draft.options.threads = '8';
    first.options.threads = '4';
    first.nodeAffinity.rows.push({ id: 1, key: 'pool', value: 'benchmark' });
    expect(second.driver).toBe('sysbench');
    expect(second.options.threads).toBe('1');
    expect(second.nodeAffinity.rows).toEqual([]);
    expect(createDriverDraft('sysbench').options.threads).toBe('1');
    expect(createInitialBenchmarkFormValues().driver).toBe('pgbench');
  });

  it('prepares Sysbench options and constructs the explicit request envelope', () => {
    const draft = createDriverDraft('sysbench');
    expect(prepareDriver(draft)).toEqual({
      ok: true,
      request: {
        driver: 'sysbench',
        options: { durationSeconds: 30, threads: 1, tables: 1, tableSize: 10000, initialize: true },
      },
    });
    const target = { k8sCluster: 'main', namespace: 'default', instance: 'mysql-1' };
    const values = { ...createInitialBenchmarkFormValues('sysbench'), database: ' dbname=app ' };
    expect(toCreateBenchmarkRunRequest(values, target)).toEqual({
      target, database: 'dbname=app', driver: 'sysbench',
      options: { durationSeconds: 30, threads: 1, tables: 1, tableSize: 10000, initialize: true },
    });
    expect(toCreateBenchmarkRunRequest({ ...values, database: 'bad\nname' }, target)).toBeUndefined();
    draft.options.tableSize = '';
    expect(prepareDriver(draft)).toEqual({ ok: false, errors: { tableSize: 'Rows per table must be a positive whole number.' } });
  });

  it('uses each recorded driver to adapt its own options', () => {
    const options = { initialize: false, tables: 3, tableSize: 500 };
    expect(describeRunOptions('sysbench', options)).toEqual(options);
    expect(describeRunOptions('pgbench', { initialize: false, scale: 1 })).toEqual({
      initialize: false, scale: 'From existing tables',
    });
    expect(describeRunOptions('future-driver', options)).toEqual(options);
  });
});

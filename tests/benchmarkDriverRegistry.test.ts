import { describe, expect, it } from 'vitest';
import {
  benchmarkDriverIds, benchmarkProviderNames, createDriverDraft, describeRunOptions,
  getBenchmarkDriversForProvider, prepareDriver,
} from '../src/drivers';
import { createInitialBenchmarkFormValues, toCreateBenchmarkRunRequest, validateBenchmarkForm } from '../src/benchmarkFormValidation';

describe('benchmark driver registry', () => {
  it('registers all drivers and their provider metadata', () => {
    expect(benchmarkDriverIds).toEqual(['pgbench', 'sysbench', 'go-ycsb']);
    expect(benchmarkProviderNames).toEqual([
      'provider-cloudnative-pg', 'provider-percona-postgresql', 'percona-server-mysql', 'percona-xtradb-cluster', 'percona-server-mongodb',
    ]);
    expect(getBenchmarkDriversForProvider('provider-cloudnative-pg')).toEqual(['pgbench']);
    expect(getBenchmarkDriversForProvider('provider-percona-postgresql')).toEqual(['pgbench']);
    expect(getBenchmarkDriversForProvider('percona-server-mysql')).toEqual(['sysbench']);
    expect(getBenchmarkDriversForProvider('percona-xtradb-cluster')).toEqual(['sysbench']);
    expect(getBenchmarkDriversForProvider('percona-server-mongodb')).toEqual(['go-ycsb']);
    for (const provider of [undefined, '', 'unknown', 'provider-mariadb', 'provider-percona-xtradb-cluster']) {
      expect(getBenchmarkDriversForProvider(provider)).toEqual([]);
    }
  });

  it('creates independent go-ycsb drafts with the fixed database and builds only its options', () => {
    const first = createInitialBenchmarkFormValues('go-ycsb');
    const second = createInitialBenchmarkFormValues('go-ycsb');
    if (second.driver !== 'go-ycsb') throw new Error('Expected a go-ycsb form draft');
    const selection = createDriverDraft('go-ycsb');
    selection.options.collection = 'changed';
    first.options.threads = '4';
    first.nodeAffinity.rows.push({ id: 1, key: 'pool', value: 'benchmark' });
    expect(first.database).toBe('ycsb');
    expect(second.database).toBe('ycsb');
    expect(second.options.threads).toBe('1');
    expect(second.nodeAffinity.rows).toEqual([]);
    expect(createDriverDraft('go-ycsb').options.collection).toBe('usertable');
    const target = { k8sCluster: 'main', namespace: 'default', instance: 'mongo-1' };
    expect(toCreateBenchmarkRunRequest(second, target, 'percona-server-mongodb')).toEqual({
      target, database: 'ycsb', driver: 'go-ycsb',
      options: { collection: 'usertable', recordCount: 10000, operationCount: 20000, threads: 1, initialize: true },
    });
    for (const database of ['other', 'admin', 'YCSB', 'ycsb\n']) {
      const invalid = { ...second, database };
      expect(validateBenchmarkForm(invalid).database).toBe('go-ycsb supports only the ycsb database.');
      expect(toCreateBenchmarkRunRequest(invalid, target)).toBeUndefined();
    }
    expect(toCreateBenchmarkRunRequest({ ...second, database: '' }, target)).not.toHaveProperty('database');
    const invalidOptions = { ...second, options: { ...second.options, collection: 'system.records' } };
    expect(toCreateBenchmarkRunRequest(invalidOptions, target)).toBeUndefined();
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
    const mongoOptions = { initialize: false, collection: 'existing', recordCount: 50 };
    expect(describeRunOptions('go-ycsb', mongoOptions)).toEqual(mongoOptions);
    expect(describeRunOptions('go-ycsb', mongoOptions)).not.toBe(mongoOptions);
  });
});

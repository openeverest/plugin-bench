import { describe, expect, it } from 'vitest';
import {
  describeGoYCSBRunOptions, initialGoYCSBOptionsDraft, prepareGoYCSBOptions, validateGoYCSBDatabase,
} from '../src/drivers/goycsb';
import type { GoYCSBOptionsDraft } from '../src/drivers/goycsb';

const draft = (overrides: Partial<GoYCSBOptionsDraft> = {}): GoYCSBOptionsDraft => ({
  ...initialGoYCSBOptionsDraft, ...overrides,
});

describe('go-ycsb options', () => {
  it('starts with a fresh dataset and the backend dataset/worker defaults', () => {
    expect(initialGoYCSBOptionsDraft).toEqual({
      collection: 'usertable', recordCount: '10000', operationCount: '20000', threads: '1', initialize: true,
    });
    expect(prepareGoYCSBOptions(draft())).toEqual({
      ok: true,
      options: { collection: 'usertable', recordCount: 10000, operationCount: 20000, threads: 1, initialize: true },
    });
  });

  it.each([true, false])('keeps explicit dataset settings with initialize=%s without mutating the draft', initialize => {
    const input = Object.freeze(draft({
      collection: ' benchmark.records ', recordCount: ' 0500 ', operationCount: '1000', threads: '4', initialize,
    }));
    expect(prepareGoYCSBOptions(input)).toEqual({
      ok: true,
      options: { collection: ' benchmark.records ', recordCount: 500, operationCount: 1000, threads: 4, initialize },
    });
    expect(input.recordCount).toBe(' 0500 ');
    expect(input.collection).toBe(' benchmark.records ');
    expect(draft().threads).toBe('1');
  });

  it.each(['1', '2147483647'])('accepts the integer boundary %s', value => {
    expect(prepareGoYCSBOptions(draft({ recordCount: value, operationCount: value, threads: value })).ok).toBe(true);
  });

  describe.each(['recordCount', 'operationCount', 'threads'] as const)('%s', field => {
    it.each(['', ' ', '0', '-1', '1.5', '1e3', '+1', 'NaN', 'Infinity', 'abc', '2147483648', '999999999999999999999999'])(
      'rejects invalid input %j', value => {
        const result = prepareGoYCSBOptions(draft({ [field]: value }));
        expect(result.ok).toBe(false);
        if (result.ok === false) expect(result.errors[field]).toEqual(expect.any(String));
      },
    );
  });

  it.each([true, false])('requires record count even with initialize=%s', initialize => {
    expect(prepareGoYCSBOptions(draft({ initialize, recordCount: '' }))).toEqual({
      ok: false, errors: { recordCount: 'Records must be a positive whole number.' },
    });
  });

  it.each([true, false])('requires threads not to exceed operations with initialize=%s', initialize => {
    expect(prepareGoYCSBOptions(draft({ initialize, operationCount: '2', threads: '3' }))).toEqual({
      ok: false, errors: { threads: 'Threads cannot exceed operations.' },
    });
    expect(prepareGoYCSBOptions(draft({ initialize, operationCount: '2', threads: '2' })).ok).toBe(true);
  });

  it('retains an invalid operation error rather than inventing a thread relationship error', () => {
    const result = prepareGoYCSBOptions(draft({ operationCount: '0', threads: '4' }));
    expect(result).toEqual({ ok: false, errors: { operationCount: 'Operations must be greater than zero.' } });
  });

  it.each([
    'usertable', 'benchmark.records', ' spaced collection ', '資料', '\uFEFF',
    'a'.repeat(230), 'é'.repeat(115), 'a'.repeat(229) + 'b',
  ])('preserves a valid collection name %j', collection => {
    const result = prepareGoYCSBOptions(draft({ collection }));
    expect(result.ok).toBe(true);
    if (result.ok) expect(result.options.collection).toBe(collection);
  });

  it.each([
    '', ' ', '\t', '\u00A0', 'system.records', 'records$', 'bad\0name', 'bad\nname',
    'bad\u007Fname', 'bad\u0085name', '.records', 'records.', 'bad..records',
    'a'.repeat(231), 'é'.repeat(116), 'a'.repeat(229) + 'é',
  ])('rejects an invalid collection name %j', collection => {
    const result = prepareGoYCSBOptions(draft({ collection }));
    expect(result.ok).toBe(false);
    if (result.ok === false) expect(result.errors.collection).toEqual(expect.any(String));
  });

  it('reports collection and numeric errors together without echoing submitted content', () => {
    const result = prepareGoYCSBOptions(draft({ collection: 'system.credential-sentinel', threads: 'bad' }));
    expect(result.ok).toBe(false);
    if (result.ok === false) {
      expect(Object.keys(result.errors).sort()).toEqual(['collection', 'threads']);
      expect(JSON.stringify(result.errors)).not.toContain('credential-sentinel');
    }
  });

  it('describes recorded options without changing the original or discarding reused dataset settings', () => {
    const recorded = Object.freeze({ collection: 'existing', recordCount: 123, operationCount: 456, threads: 2, initialize: false });
    const described = describeGoYCSBRunOptions(recorded);
    expect(described).toEqual(recorded);
    expect(described).not.toBe(recorded);
    described.recordCount = 999;
    expect(recorded.recordCount).toBe(123);
  });
});

describe('go-ycsb database policy', () => {
  it.each(['', ' ', 'ycsb', ' ycsb ', '\u00A0ycsb\u2003'])('accepts the backend default or ycsb in %j', database => {
    expect(validateGoYCSBDatabase(database)).toBeUndefined();
  });

  it.each([
    'other', 'YCSB', 'admin', 'mongodb://host/ycsb', 'credential-sentinel',
    'ycsb\n', '\tycsb', 'ycsb\0', '\u0085ycsb', '\uFEFFycsb', '\uFEFF',
  ])('rejects an unsupported database value %j', database => {
    expect(validateGoYCSBDatabase(database)).toBe('go-ycsb supports only the ycsb database.');
  });
});

import { describe, expect, it } from 'vitest';
import {
  describeSysbenchRunOptions,
  initialSysbenchOptionsDraft,
  prepareSysbenchOptions,
  validateSysbenchDatabase,
} from '../src/drivers/sysbench';
import type { SysbenchOptionField } from '../src/drivers/sysbench';

const numericFields: readonly SysbenchOptionField[] = ['durationSeconds', 'threads', 'tables', 'tableSize'];

describe('Sysbench options', () => {
  it('defaults the UI to preparing fresh tables', () => {
    expect(initialSysbenchOptionsDraft).toEqual({
      durationSeconds: '30', threads: '1', tables: '1', tableSize: '10000', initialize: true,
    });
    expect(prepareSysbenchOptions(initialSysbenchOptionsDraft)).toEqual({
      ok: true,
      options: { durationSeconds: 30, threads: 1, tables: 1, tableSize: 10000, initialize: true },
    });
  });

  it.each([false, true])('converts edited values without changing the draft (initialize=%s)', initialize => {
    const draft = {
      durationSeconds: ' 5 ', threads: ' 4 ', tables: ' 2 ', tableSize: ' 00500 ', initialize,
    };
    const before = { ...draft };
    expect(prepareSysbenchOptions(draft)).toEqual({
      ok: true,
      options: { durationSeconds: 5, threads: 4, tables: 2, tableSize: 500, initialize },
    });
    expect(draft).toEqual(before);
    expect(initialSysbenchOptionsDraft.tableSize).toBe('10000');
  });

  it.each(numericFields)('accepts both integer boundaries for %s', field => {
    for (const value of ['1', '2147483647']) {
      const result = prepareSysbenchOptions({ ...initialSysbenchOptionsDraft, [field]: value });
      if (!result.ok) throw new Error(`Rejected valid ${field}: ${value}`);
      expect(result.options[field]).toBe(Number(value));
    }
  });

  it.each(numericFields)('rejects invalid %s with preparation on or off', field => {
    for (const initialize of [false, true]) {
      for (const value of ['', ' \t ', '0', '-1', '1.5', '1e3', '0x10', 'abc', 'Infinity', '2147483648', '999999999999999999999']) {
        const draft = { ...initialSysbenchOptionsDraft, initialize, [field]: value };
        const before = { ...draft };
        const result = prepareSysbenchOptions(draft);
        if (result.ok) throw new Error(`Accepted invalid ${field}: ${JSON.stringify(value)} (initialize=${initialize})`);
        expect(Object.keys(result.errors)).toEqual([field]);
        expect(result.errors[field]).toBeTruthy();
        expect(result).not.toHaveProperty('options');
        expect(draft).toEqual(before);
      }
    }
  });

  it('reports all invalid fields together', () => {
    const result = prepareSysbenchOptions({
      durationSeconds: '', threads: '0', tables: '-1', tableSize: '1.5', initialize: false,
    });
    expect(result).toEqual({
      ok: false,
      errors: {
        durationSeconds: 'Duration must be a positive whole number.',
        threads: 'Threads must be greater than zero.',
        tables: 'Tables must be a positive whole number.',
        tableSize: 'Rows per table must be a positive whole number.',
      },
    });
  });
});

describe('Sysbench database validation', () => {
  it.each(['', ' \t ', 'sbtest', ' custom ', 'dbname=app', 'my database'])('accepts %j', database => {
    expect(validateSysbenchDatabase(database)).toBeUndefined();
  });

  it.each(['app\x00db', 'app\rdb', 'app\ndb', 'app\n', '\rapp'])('rejects controls in %j before trimming', database => {
    expect(validateSysbenchDatabase(database)).toBe('Database must not contain NUL bytes or line breaks.');
  });

  it.each([`a'" b`, `a'",b`, `'a"b`, `"a'b`, `#a'"b`, `a'"\u0085b`])(
    'rejects unsupported quoting in %j', database => {
      expect(validateSysbenchDatabase(database)).toBe('Database cannot be represented in the Sysbench configuration file.');
    },
  );

  it('matches the configuration byte limit for quoted and unquoted names', () => {
    for (const database of ['a'.repeat(242), 'é'.repeat(121), 'a'.repeat(242) + `'"`]) {
      expect(validateSysbenchDatabase(database)).toBeUndefined();
      expect(validateSysbenchDatabase(` \u0085${database}\u0085 `)).toBeUndefined();
      expect(validateSysbenchDatabase(database + 'a')).toBe('Database exceeds the Sysbench configuration line limit.');
    }
  });
});

describe('Sysbench recorded options', () => {
  it.each([false, true])('preserves the effective dataset settings (initialize=%s)', initialize => {
    const options = Object.freeze({
      durationSeconds: 5, threads: 2, tables: 3, tableSize: 500, initialize,
      futureOption: { mode: 'read' }, extraCount: 0,
    });
    const display = describeSysbenchRunOptions(options);
    expect(display).toEqual(options);
    expect(display).not.toBe(options);
    display.tableSize = 10000;
    expect(options.tableSize).toBe(500);
  });

  it('preserves an empty snapshot', () => {
    expect(describeSysbenchRunOptions({})).toEqual({});
  });
});

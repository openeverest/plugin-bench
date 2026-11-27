import { describe, expect, it } from 'vitest';
import {
  initialHammerDBOptionsDraft,
  prepareHammerDBOptions,
  validateHammerDBDatabase,
} from '../src/drivers/hammerdb';
import type { HammerDBOptionField } from '../src/drivers/hammerdb';

describe('HammerDB options', () => {
  it('defaults the UI to building a fresh one-warehouse schema', () => {
    expect(prepareHammerDBOptions(initialHammerDBOptionsDraft)).toEqual({
      ok: true,
      options: { warehouses: 1, virtualUsers: 1, rampupMinutes: 0, durationMinutes: 1, initialize: true },
    });
  });

  it.each([false, true])('converts edited values without changing the draft (initialize=%s)', initialize => {
    const draft = { warehouses: ' 008 ', virtualUsers: ' 4 ', rampupMinutes: ' 2 ', durationMinutes: '5', initialize };
    const before = { ...draft };
    expect(prepareHammerDBOptions(draft)).toEqual({
      ok: true,
      options: { warehouses: 8, virtualUsers: 4, rampupMinutes: 2, durationMinutes: 5, initialize },
    });
    expect(draft).toEqual(before);
  });

  it.each<[HammerDBOptionField, string[], string[]]>([
    ['warehouses', ['1', '2147483647'], ['0', '2147483648']],
    ['virtualUsers', ['1'], ['0']],
    ['rampupMinutes', ['0', '153722866'], ['153722867']],
    ['durationMinutes', ['1', '153722867'], ['0', '153722868']],
  ])('enforces the backend bounds for %s', (field, valid, invalid) => {
    const draft = { ...initialHammerDBOptionsDraft, warehouses: '2147483647' };
    for (const value of valid) {
      const result = prepareHammerDBOptions({ ...draft, [field]: value });
      if (!result.ok) throw new Error(`Rejected valid ${field}: ${value}: ${JSON.stringify(result.errors)}`);
      expect(result.options[field]).toBe(Number(value));
    }
    for (const value of [...invalid, '', ' ', '-1', '1.5', '1e3', 'abc']) {
      const result = prepareHammerDBOptions({ ...draft, [field]: value });
      if (result.ok) throw new Error(`Accepted invalid ${field}: ${JSON.stringify(value)}`);
      expect(Object.keys(result.errors)).toEqual([field === 'rampupMinutes' && value === '153722867' ? 'durationMinutes' : field]);
    }
  });

  it('rejects more virtual users than warehouses', () => {
    expect(prepareHammerDBOptions({ ...initialHammerDBOptionsDraft, warehouses: '2', virtualUsers: '3' })).toEqual({
      ok: false, errors: { virtualUsers: 'Virtual users cannot exceed warehouses.' },
    });
    expect(prepareHammerDBOptions({ ...initialHammerDBOptionsDraft, warehouses: '3', virtualUsers: '3' }).ok).toBe(true);
  });

  it('reports all invalid fields together', () => {
    expect(prepareHammerDBOptions({
      warehouses: '0', virtualUsers: '', rampupMinutes: '-1', durationMinutes: '1.5', initialize: false,
    })).toEqual({
      ok: false,
      errors: {
        warehouses: 'Warehouses must be greater than zero.',
        virtualUsers: 'Virtual users must be a positive whole number.',
        rampupMinutes: 'Ramp-up must be a non-negative whole number.',
        durationMinutes: 'Duration must be a positive whole number.',
      },
    });
  });
});

describe('HammerDB database validation', () => {
  it.each(['', '  ', 'tpcc', ' hammer_db-1.test '])('accepts %j', database => {
    expect(validateHammerDBDatabase(database, 'provider-cloudnative-pg')).toBeUndefined();
  });

  it.each(['my db', 'db;drop', 'db$', 'base/name', 'bäse'])('rejects %j', database => {
    expect(validateHammerDBDatabase(database)).toBe('Database may contain only letters, digits, underscores, hyphens, and dots.');
  });

  it('applies the PostgreSQL and MySQL name limits', () => {
    for (const provider of ['provider-cloudnative-pg', 'provider-percona-postgresql']) {
      expect(validateHammerDBDatabase('a'.repeat(63), provider)).toBeUndefined();
      expect(validateHammerDBDatabase('a'.repeat(64), provider)).toBe('Database must not exceed 63 characters.');
    }
    for (const provider of ['percona-server-mysql', 'percona-xtradb-cluster']) {
      expect(validateHammerDBDatabase('a'.repeat(64), provider)).toBeUndefined();
      expect(validateHammerDBDatabase('a'.repeat(65), provider)).toBe('Database must not exceed 64 characters.');
    }
  });
});

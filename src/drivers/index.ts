import {
  initialPgbenchOptionsDraft, preparePgbenchOptions, validatePgbenchDatabase,
} from './pgbench.js';
import type { PgbenchOptions, PgbenchOptionsDraft } from './pgbench';

// Register each driver's draft and validated options together so their types
// stay associated throughout the form and the submitted request.
type DriverTypes = {
  pgbench: { draft: PgbenchOptionsDraft; options: PgbenchOptions };
};

export type BenchmarkDriverId = keyof DriverTypes;
export type DriverDraft<K extends BenchmarkDriverId = BenchmarkDriverId> = {
  [Id in K]: { driver: Id; options: DriverTypes[Id]['draft'] };
}[K];
export type DriverRequest<K extends BenchmarkDriverId = BenchmarkDriverId> = {
  [Id in K]: { driver: Id; options: DriverTypes[Id]['options'] };
}[K];
export type DriverOptionsErrors = Readonly<Record<string, string>>;
type DriverPreparation<K extends BenchmarkDriverId> =
  | { ok: true; request: DriverRequest<K> }
  | { ok: false; errors: DriverOptionsErrors };

type DriverDefinition<K extends BenchmarkDriverId> = {
  label: string;
  createDraft: () => DriverDraft<K>;
  prepare: (draft: DriverDraft<K>) => DriverPreparation<K>;
  validateDatabase: (database: string) => string | undefined;
};

export const benchmarkDrivers: { [K in BenchmarkDriverId]: DriverDefinition<K> } = {
  pgbench: {
    label: 'pgbench',
    createDraft: () => ({ driver: 'pgbench', options: { ...initialPgbenchOptionsDraft } }),
    prepare: ({ options }) => {
      const prepared = preparePgbenchOptions(options);
      if (prepared.ok === false) return prepared;
      return { ok: true, request: { driver: 'pgbench', options: prepared.options } };
    },
    validateDatabase: validatePgbenchDatabase,
  },
};

export const benchmarkDriverIds = Object.keys(benchmarkDrivers) as BenchmarkDriverId[];
export const defaultBenchmarkDriver: BenchmarkDriverId = 'pgbench';

export function isBenchmarkDriverId(value: string): value is BenchmarkDriverId {
  return Object.prototype.hasOwnProperty.call(benchmarkDrivers, value);
}

export function createDriverDraft<K extends BenchmarkDriverId>(driver: K): DriverDraft<K> {
  return benchmarkDrivers[driver].createDraft();
}

export function prepareDriver<K extends BenchmarkDriverId>(draft: DriverDraft<K>): DriverPreparation<K> {
  return benchmarkDrivers[draft.driver].prepare(draft);
}

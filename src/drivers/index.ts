import {
  describePgbenchRunOptions, initialPgbenchOptionsDraft, preparePgbenchOptions, validatePgbenchDatabase,
} from './pgbench.js';
import type { PgbenchOptions, PgbenchOptionsDraft } from './pgbench';
import {
  describeSysbenchRunOptions, initialSysbenchOptionsDraft, prepareSysbenchOptions, validateSysbenchDatabase,
} from './sysbench.js';
import type { SysbenchOptions, SysbenchOptionsDraft } from './sysbench';
import {
  describeGoYCSBRunOptions, initialGoYCSBOptionsDraft, prepareGoYCSBOptions, validateGoYCSBDatabase,
} from './goycsb.js';
import type { GoYCSBOptions, GoYCSBOptionsDraft } from './goycsb';
import {
  describeHammerDBRunOptions, hammerdbMySQLProviders, hammerdbPostgresProviders, initialHammerDBOptionsDraft,
  prepareHammerDBOptions, validateHammerDBDatabase,
} from './hammerdb.js';
import type { HammerDBOptions, HammerDBOptionsDraft } from './hammerdb';

// Register each driver's draft and validated options together so their types
// stay associated throughout the form and the submitted request.
type DriverTypes = {
  pgbench: { draft: PgbenchOptionsDraft; options: PgbenchOptions };
  sysbench: { draft: SysbenchOptionsDraft; options: SysbenchOptions };
  mariadb: { draft: SysbenchOptionsDraft; options: SysbenchOptions };
  'go-ycsb': { draft: GoYCSBOptionsDraft; options: GoYCSBOptions };
  hammerdb: { draft: HammerDBOptionsDraft; options: HammerDBOptions };
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
  providers: readonly string[];
  /** Providers whose credentials do not supply a default database. */
  databaseRequiredProviders?: readonly string[];
  /** A driver whose tool can benchmark only one database. */
  fixedDatabase?: { name: string; helperText: string };
  createDraft: () => DriverDraft<K>;
  prepare: (draft: DriverDraft<K>) => DriverPreparation<K>;
  validateDatabase: (database: string, provider?: string) => string | undefined;
  /** Adapts a recorded options snapshot for display without changing the run record. */
  describeRunOptions: (options: Readonly<Record<string, unknown>>) => Record<string, unknown>;
};

export const benchmarkDrivers: { [K in BenchmarkDriverId]: DriverDefinition<K> } = {
  pgbench: {
    label: 'pgbench',
    providers: ['provider-cloudnative-pg', 'provider-percona-postgresql'],
    createDraft: () => ({ driver: 'pgbench', options: { ...initialPgbenchOptionsDraft } }),
    prepare: ({ options }) => {
      const prepared = preparePgbenchOptions(options);
      if (prepared.ok === false) return prepared;
      return { ok: true, request: { driver: 'pgbench', options: prepared.options } };
    },
    validateDatabase: validatePgbenchDatabase,
    describeRunOptions: describePgbenchRunOptions,
  },
  sysbench: {
    label: 'Sysbench',
    providers: ['percona-server-mysql', 'percona-xtradb-cluster'],
    databaseRequiredProviders: ['percona-server-mysql', 'percona-xtradb-cluster'],
    createDraft: () => ({ driver: 'sysbench', options: { ...initialSysbenchOptionsDraft } }),
    prepare: ({ options }) => {
      const prepared = prepareSysbenchOptions(options);
      if (prepared.ok === false) return prepared;
      return { ok: true, request: { driver: 'sysbench', options: prepared.options } };
    },
    validateDatabase: validateSysbenchDatabase,
    describeRunOptions: describeSysbenchRunOptions,
  },
  mariadb: {
    label: 'Sysbench (MariaDB)',
    providers: ['mariadb'],
    createDraft: () => ({ driver: 'mariadb', options: { ...initialSysbenchOptionsDraft } }),
    prepare: ({ options }) => {
      const prepared = prepareSysbenchOptions(options);
      if (prepared.ok === false) return prepared;
      return { ok: true, request: { driver: 'mariadb', options: prepared.options } };
    },
    validateDatabase: validateSysbenchDatabase,
    describeRunOptions: describeSysbenchRunOptions,
  },
  'go-ycsb': {
    label: 'go-ycsb',
    providers: ['percona-server-mongodb'],
    fixedDatabase: {
      name: 'ycsb',
      helperText: 'go-ycsb currently supports only the ycsb database. The instance credentials need read/write access to it.',
    },
    createDraft: () => ({ driver: 'go-ycsb', options: { ...initialGoYCSBOptionsDraft } }),
    prepare: ({ options }) => {
      const prepared = prepareGoYCSBOptions(options);
      if (prepared.ok === false) return prepared;
      return { ok: true, request: { driver: 'go-ycsb', options: prepared.options } };
    },
    validateDatabase: validateGoYCSBDatabase,
    describeRunOptions: describeGoYCSBRunOptions,
  },
  // Listed after the default driver of each provider so it is never preselected.
  hammerdb: {
    label: 'HammerDB',
    providers: [...hammerdbPostgresProviders, ...hammerdbMySQLProviders],
    databaseRequiredProviders: hammerdbMySQLProviders,
    createDraft: () => ({ driver: 'hammerdb', options: { ...initialHammerDBOptionsDraft } }),
    prepare: ({ options }) => {
      const prepared = prepareHammerDBOptions(options);
      if (prepared.ok === false) return prepared;
      return { ok: true, request: { driver: 'hammerdb', options: prepared.options } };
    },
    validateDatabase: validateHammerDBDatabase,
    describeRunOptions: describeHammerDBRunOptions,
  },
};

export const benchmarkDriverIds = Object.keys(benchmarkDrivers) as BenchmarkDriverId[];
export const benchmarkProviderNames = [...new Set(benchmarkDriverIds.flatMap(driver => benchmarkDrivers[driver].providers))];
export const defaultBenchmarkDriver: BenchmarkDriverId = 'pgbench';

export function getBenchmarkDriversForProvider(provider: string | undefined): BenchmarkDriverId[] {
  if (!provider) return [];
  return benchmarkDriverIds.filter(driver => benchmarkDrivers[driver].providers.includes(provider));
}

export function isBenchmarkDriverId(value: string): value is BenchmarkDriverId {
  return Object.prototype.hasOwnProperty.call(benchmarkDrivers, value);
}

export function createDriverDraft<K extends BenchmarkDriverId>(driver: K): DriverDraft<K> {
  return benchmarkDrivers[driver].createDraft();
}

export function prepareDriver<K extends BenchmarkDriverId>(draft: DriverDraft<K>): DriverPreparation<K> {
  return benchmarkDrivers[draft.driver].prepare(draft);
}

// Snapshots from drivers this UI does not know are displayed as recorded.
export function describeRunOptions(
  driver: string | undefined,
  options: Readonly<Record<string, unknown>>,
): Record<string, unknown> {
  return driver !== undefined && isBenchmarkDriverId(driver)
    ? benchmarkDrivers[driver].describeRunOptions(options)
    : { ...options };
}

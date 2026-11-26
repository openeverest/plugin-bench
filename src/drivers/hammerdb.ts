/** Validated options for the HammerDB runner's TPROC-C workload. */
export type HammerDBOptions = {
  warehouses: number;
  virtualUsers: number;
  rampupMinutes: number;
  durationMinutes: number;
  initialize: boolean;
};

/** Editable input values stay strings until validation succeeds. */
export type HammerDBOptionsDraft = {
  warehouses: string;
  virtualUsers: string;
  rampupMinutes: string;
  durationMinutes: string;
  initialize: boolean;
};

export type HammerDBOptionField = Exclude<keyof HammerDBOptionsDraft, 'initialize'>;
export type HammerDBOptionsErrors = Partial<Record<HammerDBOptionField, string>>;

export const hammerdbPostgresProviders = ['provider-cloudnative-pg', 'provider-percona-postgresql'] as const;
export const hammerdbMySQLProviders = ['percona-server-mysql', 'percona-xtradb-cluster'] as const;

// The backend defaults initialize to false; the UI prepares a fresh dataset by
// default like the other drivers.
export const initialHammerDBOptionsDraft: Readonly<HammerDBOptionsDraft> = {
  warehouses: '1',
  virtualUsers: '1',
  rampupMinutes: '0',
  durationMinutes: '1',
  initialize: true,
};

// Keep aligned with validName and databaseLimit in the backend HammerDB driver.
export function validateHammerDBDatabase(database: string, provider?: string): string | undefined {
  const name = database.trim();
  if (name === '') return undefined;
  if (!/^[A-Za-z0-9_.-]+$/.test(name)) {
    return 'Database may contain only letters, digits, underscores, hyphens, and dots.';
  }
  const limit = (hammerdbPostgresProviders as readonly string[]).includes(provider ?? '') ? 63 : 64;
  if (name.length > limit) return `Database must not exceed ${limit} characters.`;
  return undefined;
}

// Reused datasets must match the recorded warehouse count, so it stays accurate.
export function describeHammerDBRunOptions(options: Readonly<Record<string, unknown>>): Record<string, unknown> {
  return { ...options };
}

export function prepareHammerDBOptions(draft: HammerDBOptionsDraft):
  | { ok: true; options: HammerDBOptions }
  | { ok: false; errors: HammerDBOptionsErrors } {
  const errors: HammerDBOptionsErrors = {};
  const maxInt32 = 2_147_483_647;
  const fields: ReadonlyArray<[HammerDBOptionField, string, number]> = [
    ['warehouses', 'Warehouses', 1], ['virtualUsers', 'Virtual users', 1],
    ['rampupMinutes', 'Ramp-up', 0], ['durationMinutes', 'Duration', 1],
  ];
  for (const [field, label, minimum] of fields) {
    const value = draft[field].trim();
    if (!/^\d+$/.test(value)) {
      errors[field] = `${label} must be a ${minimum === 0 ? 'non-negative' : 'positive'} whole number.`;
    } else if (Number(value) < minimum) {
      // Only fields with a minimum of one can reach this branch.
      errors[field] = `${label} must be greater than zero.`;
    } else if (Number(value) > maxInt32) {
      errors[field] = `${label} must not exceed ${maxInt32}.`;
    }
  }
  if (!errors.warehouses && !errors.virtualUsers && Number(draft.virtualUsers) > Number(draft.warehouses)) {
    errors.virtualUsers = 'Virtual users cannot exceed warehouses.';
  }
  // The runner converts the combined period to a Go time.Duration.
  const maxTotalMinutes = 153_722_867;
  if (!errors.rampupMinutes && !errors.durationMinutes &&
      Number(draft.rampupMinutes) + Number(draft.durationMinutes) > maxTotalMinutes) {
    errors.durationMinutes = `Ramp-up plus duration must not exceed ${maxTotalMinutes} minutes.`;
  }
  if (Object.keys(errors).length > 0) return { ok: false, errors };
  return {
    ok: true,
    options: {
      warehouses: Number(draft.warehouses),
      virtualUsers: Number(draft.virtualUsers),
      rampupMinutes: Number(draft.rampupMinutes),
      durationMinutes: Number(draft.durationMinutes),
      initialize: draft.initialize,
    },
  };
}

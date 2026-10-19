/** Validated pgbench options sent to the backend. */
export type PgbenchOptions = {
  durationSeconds: number;
  clients: number;
  threads: number;
  scale: number;
  initialize: boolean;
};

/** Editable input values stay strings until validation succeeds. */
export type PgbenchOptionsDraft = {
  durationSeconds: string;
  clients: string;
  threads: string;
  scale: string;
  initialize: boolean;
};

export type PgbenchOptionField = Exclude<keyof PgbenchOptionsDraft, 'initialize'>;
export type PgbenchOptionsErrors = Partial<Record<PgbenchOptionField, string>>;

// Preserve the UI's explicit choices; omitted API options have backend defaults.
export const initialPgbenchOptionsDraft: Readonly<PgbenchOptionsDraft> = {
  durationSeconds: '30',
  clients: '1',
  threads: '1',
  scale: '1',
  initialize: true,
};

export function validatePgbenchDatabase(database: string): string | undefined {
  const name = database.trim();
  if (name.includes('=') || /^postgres(?:ql)?:\/\//i.test(name)) {
    return 'Enter a database name, not a connection string.';
  }
  return undefined;
}

export function preparePgbenchOptions(draft: PgbenchOptionsDraft):
  | { ok: true; options: PgbenchOptions }
  | { ok: false; errors: PgbenchOptionsErrors } {
  const errors: PgbenchOptionsErrors = {};
  const maxInt32 = 2_147_483_647;
  const fields: ReadonlyArray<[PgbenchOptionField, string]> = [
    ['durationSeconds', 'Duration'], ['clients', 'Clients'],
    ['threads', 'Threads'], ['scale', 'Scale'],
  ];
  for (const [field, label] of fields) {
    if (field === 'scale' && !draft.initialize) continue;
    const value = draft[field].trim();
    if (!/^\d+$/.test(value)) {
      errors[field] = `${label} must be a positive whole number.`;
    } else if (Number(value) < 1) {
      errors[field] = `${label} must be greater than zero.`;
    } else if (Number(value) > maxInt32) {
      errors[field] = `${label} must not exceed ${maxInt32}.`;
    }
  }
  if (!errors.clients && !errors.threads && Number(draft.threads) > Number(draft.clients)) {
    errors.threads = 'Threads cannot exceed clients.';
  }
  if (Object.keys(errors).length > 0) return { ok: false, errors };
  return {
    ok: true,
    options: {
      durationSeconds: Number(draft.durationSeconds),
      clients: Number(draft.clients),
      threads: Number(draft.threads),
      scale: draft.initialize ? Number(draft.scale) : 1,
      initialize: draft.initialize,
    },
  };
}

/** Validated options for the Sysbench runner's oltp_read_write workload. */
export type SysbenchOptions = {
  durationSeconds: number;
  threads: number;
  tables: number;
  tableSize: number;
  initialize: boolean;
};

/** Editable input values stay strings until validation succeeds. */
export type SysbenchOptionsDraft = {
  durationSeconds: string;
  threads: string;
  tables: string;
  tableSize: string;
  initialize: boolean;
};

export type SysbenchOptionField = Exclude<keyof SysbenchOptionsDraft, 'initialize'>;
export type SysbenchOptionsErrors = Partial<Record<SysbenchOptionField, string>>;

export const initialSysbenchOptionsDraft: Readonly<SysbenchOptionsDraft> = {
  durationSeconds: '60',
  threads: '1',
  tables: '1',
  tableSize: '10000',
  initialize: false,
};

export function validateSysbenchDatabase(database: string): string | undefined {
  if (/[\x00\r\n]/.test(database)) {
    return 'Database must not contain NUL bytes or line breaks.';
  }
  return undefined;
}

// Both dataset settings still control query generation when reusing tables.
export function describeSysbenchRunOptions(options: Readonly<Record<string, unknown>>): Record<string, unknown> {
  return { ...options };
}

export function prepareSysbenchOptions(draft: SysbenchOptionsDraft):
  | { ok: true; options: SysbenchOptions }
  | { ok: false; errors: SysbenchOptionsErrors } {
  const errors: SysbenchOptionsErrors = {};
  const maxInt32 = 2_147_483_647;
  const fields: ReadonlyArray<[SysbenchOptionField, string]> = [
    ['durationSeconds', 'Duration'], ['threads', 'Threads'],
    ['tables', 'Tables'], ['tableSize', 'Rows per table'],
  ];
  for (const [field, label] of fields) {
    const value = draft[field].trim();
    if (!/^\d+$/.test(value)) {
      errors[field] = `${label} must be a positive whole number.`;
    } else if (Number(value) < 1) {
      errors[field] = `${label} must be greater than zero.`;
    } else if (Number(value) > maxInt32) {
      errors[field] = `${label} must not exceed ${maxInt32}.`;
    }
  }
  if (Object.keys(errors).length > 0) return { ok: false, errors };
  return {
    ok: true,
    options: {
      durationSeconds: Number(draft.durationSeconds),
      threads: Number(draft.threads),
      tables: Number(draft.tables),
      tableSize: Number(draft.tableSize),
      initialize: draft.initialize,
    },
  };
}

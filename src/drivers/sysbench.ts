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
  durationSeconds: '30',
  threads: '1',
  tables: '1',
  tableSize: '10000',
  initialize: true,
};

export function validateSysbenchDatabase(database: string): string | undefined {
  if (/[\x00\r\n]/.test(database)) {
    return 'Database must not contain NUL bytes or line breaks.';
  }
  // Match backend trimming after the request builder trims the input.
  const value = database.trim().replace(/^\p{White_Space}+|\p{White_Space}+$/gu, '');
  let quoteBytes = 2;
  if (value.includes("'") && value.includes('"')) {
    if (/[,\p{White_Space}]/u.test(value) || /^['"#]/.test(value)) {
      return 'Database cannot be represented in the Sysbench configuration file.';
    }
    quoteBytes = 0;
  }
  // Keep aligned with validateConnectionValue in the backend Sysbench driver.
  // Go counts UTF-8 bytes, including mysql-db=, optional quotes, and a newline.
  const lineBytes = 'mysql-db='.length + new TextEncoder().encode(value).length + quoteBytes + 1;
  if (lineBytes > 254) {
    return 'Database exceeds the Sysbench configuration line limit.';
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

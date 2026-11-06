/** Validated options for the go-ycsb runner's fixed MongoDB Workload A. */
export type GoYCSBOptions = {
  collection: string;
  recordCount: number;
  operationCount: number;
  threads: number;
  initialize: boolean;
};

/** Numeric inputs stay editable strings until preparation succeeds. */
export type GoYCSBOptionsDraft = {
  collection: string;
  recordCount: string;
  operationCount: string;
  threads: string;
  initialize: boolean;
};

export type GoYCSBOptionField = Exclude<keyof GoYCSBOptionsDraft, 'initialize'>;
export type GoYCSBOptionsErrors = Partial<Record<GoYCSBOptionField, string>>;

// Load a fresh dataset in the UI; the API's omitted initialize option is false.
export const initialGoYCSBOptionsDraft: Readonly<GoYCSBOptionsDraft> = {
  collection: 'usertable',
  recordCount: '10000',
  operationCount: '20000',
  threads: '1',
  initialize: true,
};

export function validateGoYCSBDatabase(database: string): string | undefined {
  // Match Go's unicode.IsControl and strings.TrimSpace without treating the
  // byte-order mark as whitespace (JavaScript's trim() would remove it).
  const name = database.replace(/^\p{White_Space}+|\p{White_Space}+$/gu, '');
  if (/\p{Cc}/u.test(database) || (name !== '' && name !== 'ycsb')) {
    return 'go-ycsb supports only the ycsb database.';
  }
  return undefined;
}

// Collection and record count still define the dataset when loading is off.
export function describeGoYCSBRunOptions(options: Readonly<Record<string, unknown>>): Record<string, unknown> {
  return { ...options };
}

export function prepareGoYCSBOptions(draft: GoYCSBOptionsDraft):
  | { ok: true; options: GoYCSBOptions }
  | { ok: false; errors: GoYCSBOptionsErrors } {
  const errors: GoYCSBOptionsErrors = {};
  const collection = draft.collection;
  if (collection.replace(/\p{White_Space}/gu, '') === '') {
    errors.collection = 'Enter a collection name.';
  } else if (collection.startsWith('system.') || collection.includes('$') || /\p{Cc}/u.test(collection) ||
      collection.startsWith('.') || collection.endsWith('.') || collection.includes('..')) {
    errors.collection = 'Collection must not start with system. or contain $, control characters, or empty dot segments.';
  } else if (new TextEncoder().encode('ycsb.' + collection).length > 235) {
    errors.collection = 'Collection namespace must not exceed 235 UTF-8 bytes.';
  }

  const maxInt32 = 2_147_483_647;
  const fields: ReadonlyArray<[Exclude<GoYCSBOptionField, 'collection'>, string]> = [
    ['recordCount', 'Records'], ['operationCount', 'Operations'], ['threads', 'Threads'],
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
  if (!errors.threads && !errors.operationCount && Number(draft.threads) > Number(draft.operationCount)) {
    errors.threads = 'Threads cannot exceed operations.';
  }
  if (Object.keys(errors).length > 0) return { ok: false, errors };
  return {
    ok: true,
    options: {
      collection,
      recordCount: Number(draft.recordCount),
      operationCount: Number(draft.operationCount),
      threads: Number(draft.threads),
      initialize: draft.initialize,
    },
  };
}

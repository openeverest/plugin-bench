import type { CreateBenchmarkRunRequest } from './benchmarkApi';
import type { BenchmarkTarget } from './benchmarkTarget';

export type BenchmarkFormValues = {
  database: string;
  durationSeconds: string;
  clients: string;
  threads: string;
  scale: string;
  initialize: boolean;
};

export const initialBenchmarkFormValues: BenchmarkFormValues = {
  database: '',
  durationSeconds: '30',
  clients: '1',
  threads: '1',
  scale: '1',
  initialize: false,
};

export type BenchmarkFormField = Exclude<keyof BenchmarkFormValues, 'initialize'>;
export type BenchmarkFormErrors = Partial<Record<BenchmarkFormField, string>>;

const maxInt32 = 2_147_483_647;

export function validateBenchmarkForm(values: BenchmarkFormValues): BenchmarkFormErrors {
  const errors: BenchmarkFormErrors = {};
  const database = values.database.trim();

  if (
    database.includes('=') ||
    database.toLowerCase().startsWith('postgres://') ||
    database.toLowerCase().startsWith('postgresql://')
  ) {
    errors.database = 'Enter a database name, not a connection string.';
  }

  const numericFields: Array<[BenchmarkFormField, string]> = [
    ['durationSeconds', 'Duration'],
    ['clients', 'Clients'],
    ['threads', 'Threads'],
    ['scale', 'Scale'],
  ];
  const parsedValues: Partial<Record<BenchmarkFormField, number>> = {};

  for (const [field, label] of numericFields) {
    const value = values[field].trim();
    if (!/^\d+$/.test(value)) {
      errors[field] = `${label} must be a positive whole number.`;
      continue;
    }

    const parsed = Number(value);
    if (parsed < 1) {
      errors[field] = `${label} must be greater than zero.`;
    } else if (parsed > maxInt32) {
      errors[field] = `${label} must not exceed ${maxInt32}.`;
    } else {
      parsedValues[field] = parsed;
    }
  }

  const clients = parsedValues.clients;
  const threads = parsedValues.threads;
  if (clients !== undefined && threads !== undefined && threads > clients) {
    errors.threads = 'Threads cannot exceed clients.';
  }

  return errors;
}

export function toCreateBenchmarkRunRequest(
  values: BenchmarkFormValues,
  target: BenchmarkTarget
): CreateBenchmarkRunRequest | undefined {
  if (Object.keys(validateBenchmarkForm(values)).length > 0) {
    return undefined;
  }

  const database = values.database.trim();
  return {
    target,
    ...(database ? { database } : {}),
    durationSeconds: Number(values.durationSeconds),
    clients: Number(values.clients),
    threads: Number(values.threads),
    scale: Number(values.scale),
    initialize: values.initialize,
  };
}

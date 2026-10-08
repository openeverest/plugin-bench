import type { CreateBenchmarkRunRequest } from './benchmarkApi';
import type { BenchmarkTarget } from './benchmarkTarget';

export type BenchmarkFormValues = {
  database: string;
  durationSeconds: string;
  clients: string;
  threads: string;
  scale: string;
  cpuRequest: string;
  cpuLimit: string;
  memoryRequest: string;
  memoryLimit: string;
  initialize: boolean;
};

export const initialBenchmarkFormValues: BenchmarkFormValues = {
  database: '',
  durationSeconds: '30',
  clients: '1',
  threads: '1',
  scale: '1',
  cpuRequest: '',
  cpuLimit: '',
  memoryRequest: '',
  memoryLimit: '',
  initialize: true,
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
    if (field === 'scale' && !values.initialize) continue;

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

  for (const [field, label] of [
    ['cpuRequest', 'CPU request'],
    ['cpuLimit', 'CPU limit'],
    ['memoryRequest', 'Memory request'],
    ['memoryLimit', 'Memory limit'],
  ] as const) {
    const value = values[field].trim();
    if (value === '') continue;
    if (!/^\d+$/.test(value) || !Number.isSafeInteger(Number(value)) || Number(value) < 1) {
      errors[field] = `${label} must be a positive whole number.`;
    }
  }

  if (!errors.cpuRequest && !errors.cpuLimit && values.cpuRequest.trim() && values.cpuLimit.trim() &&
      Number(values.cpuRequest) > Number(values.cpuLimit)) {
    errors.cpuLimit = 'CPU limit must be at least the CPU request.';
  }
  if (!errors.memoryRequest && !errors.memoryLimit && values.memoryRequest.trim() && values.memoryLimit.trim() &&
      Number(values.memoryRequest) > Number(values.memoryLimit)) {
    errors.memoryLimit = 'Memory limit must be at least the memory request.';
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
  const resources = {
    ...(values.cpuRequest.trim() ? { cpuRequest: `${Number(values.cpuRequest)}m` } : {}),
    ...(values.cpuLimit.trim() ? { cpuLimit: `${Number(values.cpuLimit)}m` } : {}),
    ...(values.memoryRequest.trim() ? { memoryRequest: `${Number(values.memoryRequest)}Mi` } : {}),
    ...(values.memoryLimit.trim() ? { memoryLimit: `${Number(values.memoryLimit)}Mi` } : {}),
  };
  return {
    target,
    ...(database ? { database } : {}),
    durationSeconds: Number(values.durationSeconds),
    clients: Number(values.clients),
    threads: Number(values.threads),
    scale: values.initialize ? Number(values.scale) : 1,
    initialize: values.initialize,
    ...(Object.keys(resources).length > 0 ? { resources } : {}),
  };
}

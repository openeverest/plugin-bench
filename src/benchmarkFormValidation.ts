import type { CreateBenchmarkRunRequest } from './benchmarkApi';
import type { BenchmarkTarget } from './benchmarkTarget';
import { createNodeAffinityDraft, prepareNodeAffinityDraft } from './benchmarkNodeAffinity.js';
import type { NodeAffinityDraft } from './benchmarkNodeAffinity';
import { benchmarkDrivers, createDriverDraft, defaultBenchmarkDriver, prepareDriver } from './drivers/index.js';
import type { DriverDraft, DriverOptionsErrors } from './drivers';

type SharedFormValues = {
  database: string;
  cpuRequest: string;
  cpuLimit: string;
  memoryRequest: string;
  memoryLimit: string;
  nodeAffinity: NodeAffinityDraft;
};

export type BenchmarkFormValues = SharedFormValues & DriverDraft;

export function createInitialBenchmarkFormValues(): BenchmarkFormValues {
  return {
    ...createDriverDraft(defaultBenchmarkDriver),
    database: '',
    cpuRequest: '',
    cpuLimit: '',
    memoryRequest: '',
    memoryLimit: '',
    nodeAffinity: createNodeAffinityDraft(),
  };
}

export const initialBenchmarkFormValues = createInitialBenchmarkFormValues();

export type BenchmarkFormField = Exclude<keyof SharedFormValues, 'nodeAffinity'>;
export type BenchmarkFormErrors = Partial<Record<BenchmarkFormField, string>> & { options?: DriverOptionsErrors };

export function validateBenchmarkForm(values: BenchmarkFormValues): BenchmarkFormErrors {
  const options = prepareDriver(values);
  return { ...validateSharedFields(values), ...(options.ok === false ? { options: options.errors } : {}) };
}

function validateSharedFields(values: BenchmarkFormValues): BenchmarkFormErrors {
  const errors: BenchmarkFormErrors = {};
  const databaseError = benchmarkDrivers[values.driver].validateDatabase(values.database);
  if (databaseError) errors.database = databaseError;

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
  const prepared = prepareDriver(values);
  if (prepared.ok === false || Object.keys(validateSharedFields(values)).length > 0) {
    return undefined;
  }
  const affinity = prepareNodeAffinityDraft(values.nodeAffinity);
  if (!affinity.ok) return undefined;

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
    ...prepared.request,
    ...(Object.keys(resources).length > 0 ? { resources } : {}),
    ...(affinity.nodeAffinity ? { nodeAffinity: affinity.nodeAffinity } : {}),
  };
}

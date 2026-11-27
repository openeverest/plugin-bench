export const valkeyFields = [
  { field: 'duration', label: 'Duration (seconds)', max: 300 },
  { field: 'threads', label: 'Threads', max: 128 },
  { field: 'clientsPerThread', label: 'Clients per thread', max: 128 },
  { field: 'keyCount', label: 'Key count', max: 100000 },
  { field: 'valueSize', label: 'Value size (bytes)', max: 16384 },
] as const;

type Field = typeof valkeyFields[number]['field'];
export type ValkeyOptions = Record<Field, number>;
export type ValkeyOptionsDraft = Record<Field, string>;
export const initialValkeyOptionsDraft: ValkeyOptionsDraft = {
  duration: '30', threads: '2', clientsPerThread: '10', keyCount: '10000', valueSize: '256',
};

export function prepareValkeyOptions(draft: ValkeyOptionsDraft) {
  const errors: Partial<Record<Field, string>> = {};
  const options: ValkeyOptions = {
    duration: Number(draft.duration), threads: Number(draft.threads), clientsPerThread: Number(draft.clientsPerThread),
    keyCount: Number(draft.keyCount), valueSize: Number(draft.valueSize),
  };
  for (const { field, max } of valkeyFields) {
    if (!/^\d+$/.test(draft[field].trim()) || options[field] < 1 || options[field] > max) {
      errors[field] = `Enter a whole number from 1 to ${max}.`;
    }
  }
  if (!errors.threads && !errors.clientsPerThread && options.threads * options.clientsPerThread > 128) {
    errors.clientsPerThread = 'Threads × clients per thread must not exceed 128.';
  }
  if (!errors.keyCount && !errors.valueSize && options.keyCount * options.valueSize > 64 * 1024 * 1024) {
    errors.valueSize = 'Key count × value size must not exceed 64 MiB.';
  }
  if (Object.keys(errors).length) return { ok: false, errors } as const;
  return { ok: true, options } as const;
}

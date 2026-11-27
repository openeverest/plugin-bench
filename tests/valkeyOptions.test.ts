import { expect, it } from 'vitest';
import { initialValkeyOptionsDraft, prepareValkeyOptions, valkeyFields } from '../src/drivers/valkey';

for (const { field, max } of valkeyFields) {
  it.each(['', '1.5', '0', String(max + 1)])(`rejects ${field}=%s`, value => {
    const result = prepareValkeyOptions({ ...initialValkeyOptionsDraft, [field]: value });
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.errors[field]).toBeTruthy();
  });
}

it('accepts exact limits and rejects either combined limit independently', () => {
  const draft = { duration: '300', threads: '128', clientsPerThread: '1', keyCount: '4096', valueSize: '16384' };
  expect(prepareValkeyOptions(draft).ok).toBe(true);
  expect(prepareValkeyOptions({ ...draft, clientsPerThread: '2' }).ok).toBe(false);
  expect(prepareValkeyOptions({ ...draft, keyCount: '4097' }).ok).toBe(false);
  expect(prepareValkeyOptions({ ...draft, keyCount: '100000', valueSize: '1', threads: '1', clientsPerThread: '128' }).ok).toBe(true);
});

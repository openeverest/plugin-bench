import type { ReactElement } from 'react';
import { PgbenchOptionsForm } from './PgbenchOptionsForm';
import { SysbenchOptionsForm } from './SysbenchOptionsForm';
import type { BenchmarkDriverId, DriverDraft, DriverOptionsErrors } from './index';

export interface DriverOptionsFormProps<K extends BenchmarkDriverId = BenchmarkDriverId> {
  selection: DriverDraft<K>;
  errors: DriverOptionsErrors;
  disabled: boolean;
  showErrors: boolean;
  onChange: (selection: DriverDraft<K>) => void;
}

// The shared form renders through this lookup. Adding a driver requires its
// options component here and its data definition in index.ts.
const optionsForms: { [K in BenchmarkDriverId]: (props: DriverOptionsFormProps<K>) => ReactElement } = {
  pgbench: PgbenchOptionsForm,
  sysbench: SysbenchOptionsForm,
};

export function DriverOptionsForm<K extends BenchmarkDriverId>(props: DriverOptionsFormProps<K>) {
  const OptionsForm: (props: DriverOptionsFormProps<K>) => ReactElement = optionsForms[props.selection.driver];
  return <OptionsForm {...props} />;
}

import type { CSSProperties } from 'react';
import type { PluginApi } from '@openeverest/plugin-sdk';
import { initialBenchmarkFormValues } from './benchmarkForm';
import { validateBenchmarkForm } from './benchmarkForm';
import type { BenchmarkFormField, BenchmarkFormValues } from './benchmarkForm';

const styles: Record<string, CSSProperties> = {
  card: { border: '1px solid #d9d9d9', borderRadius: 8, padding: 20, background: '#fff' },
  heading: { margin: '0 0 16px', fontSize: 18, fontWeight: 600 },
  fieldStack: { display: 'grid', gap: 14 },
  fieldGrid: { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))', gap: 14 },
  label: { display: 'grid', gap: 6, fontSize: 14, color: '#444' },
  control: { padding: '9px 10px', border: '1px solid #bbb', borderRadius: 4, background: '#f5f5f5' },
  checkboxLabel: { display: 'flex', alignItems: 'center', gap: 8, fontSize: 14, color: '#444' },
  error: { margin: 0, color: '#b42318', fontSize: 12 },
  warning: { margin: 0, padding: 12, borderRadius: 6, color: '#7a4b00', background: '#fff4d6', fontSize: 13 },
  actionRow: { display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' },
  button: { padding: '9px 16px', border: 0, borderRadius: 4, color: '#777', background: '#ddd' },
  badge: { padding: '3px 9px', border: '1px solid #bbb', borderRadius: 12, fontSize: 12, color: '#555' },
  caption: { margin: 0, color: '#666', fontSize: 12 },
};

type BenchmarkFormProps = {
  react: PluginApi['React'];
};

export function BenchmarkForm({ react }: BenchmarkFormProps) {
  const [values, setValues] = react.useState<BenchmarkFormValues>({ ...initialBenchmarkFormValues });
  const [touched, setTouched] = react.useState<Partial<Record<keyof BenchmarkFormValues, boolean>>>({});
  const errors = validateBenchmarkForm(values);
  const updateValue = <K extends keyof BenchmarkFormValues>(key: K, value: BenchmarkFormValues[K]) => {
    setValues((current) => ({ ...current, [key]: value }));
    setTouched((current) => ({ ...current, [key]: true }));
  };
  const fieldError = (field: BenchmarkFormField) => (touched[field] ? errors[field] : undefined);
  const errorMessage = (field: BenchmarkFormField) => {
    const message = fieldError(field);
    return message
      ? react.createElement('span', { id: `benchmark-${field}-error`, style: styles.error, role: 'alert' }, message)
      : null;
  };

  return react.createElement(
    'form',
      {
        style: styles.card,
        onSubmit: (event: { preventDefault: () => void }) => {
          event.preventDefault();
          setTouched({
            database: true,
            durationSeconds: true,
            clients: true,
            threads: true,
            scale: true,
            initialize: true,
          });
        },
      },
    react.createElement('h3', { style: styles.heading }, 'Run configuration'),
    react.createElement(
      'div',
      { style: styles.fieldStack },
      react.createElement('div', { style: styles.fieldGrid },
        react.createElement(
          'label',
          { htmlFor: 'benchmark-database', style: styles.label },
          'Database',
          react.createElement('input', {
            id: 'benchmark-database',
            name: 'database',
            type: 'text',
            style: styles.control,
            value: values.database,
            'aria-invalid': Boolean(fieldError('database')),
            'aria-describedby': fieldError('database') ? 'benchmark-database-error' : undefined,
            onChange: (event: { currentTarget: { value: string } }) => updateValue('database', event.currentTarget.value),
          }),
          errorMessage('database')
        ),
        ...([
          ['durationSeconds', 'Duration (seconds)'],
          ['clients', 'Clients'],
          ['threads', 'Threads'],
          ['scale', 'Scale'],
        ] as const).map(([field, label]) =>
          react.createElement(
            'label',
            { key: field, htmlFor: `benchmark-${field}`, style: styles.label },
            label,
            react.createElement('input', {
              id: `benchmark-${field}`,
              name: field,
              type: 'number',
              min: 1,
              step: 1,
              style: styles.control,
              value: values[field],
              'aria-invalid': Boolean(fieldError(field)),
              'aria-describedby': fieldError(field) ? `benchmark-${field}-error` : undefined,
              onChange: (event: { currentTarget: { value: string } }) => updateValue(field, event.currentTarget.value),
            }),
            errorMessage(field)
          )
        )
      ),
      react.createElement(
        'label',
        { htmlFor: 'benchmark-initialize', style: styles.checkboxLabel },
        react.createElement('input', {
          id: 'benchmark-initialize',
          name: 'initialize',
          type: 'checkbox',
          checked: values.initialize,
          onChange: (event: { currentTarget: { checked: boolean } }) => updateValue('initialize', event.currentTarget.checked),
        }),
        'Initialize pgbench tables'
      ),
      values.initialize && react.createElement(
        'p',
        { style: styles.warning, role: 'alert' },
        'Initializing will drop and recreate the pgbench tables in the selected database.'
      ),
      react.createElement(
        'div',
        { style: styles.actionRow },
        react.createElement('button', { type: 'submit', style: styles.button, disabled: true }, 'Start benchmark'),
        react.createElement('span', { style: styles.badge }, 'Run submission is not implemented yet')
      ),
      react.createElement(
        'p',
        { style: styles.caption },
        'Configure the run locally; benchmark submission will be added in a later step.'
      )
    )
  );
}

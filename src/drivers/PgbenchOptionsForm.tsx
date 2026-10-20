import { useState } from 'react';
import { Box, Checkbox, FormControlLabel, FormHelperText, Stack, TextField } from '@mui/material';
import type { DriverOptionsFormProps } from './DriverOptionsForm';
import type { PgbenchOptionField } from './pgbench';

const NUMERIC_FIELDS: ReadonlyArray<{ field: PgbenchOptionField; label: string; hint: string }> = [
  { field: 'durationSeconds', label: 'Duration (seconds)', hint: 'How long the benchmark runs.' },
  { field: 'clients', label: 'Clients', hint: 'Concurrent database sessions.' },
  { field: 'threads', label: 'Threads', hint: 'Worker threads, up to the number of clients.' },
  { field: 'scale', label: 'Scale', hint: 'Dataset size on initialization (×100,000 rows).' },
];

export function PgbenchOptionsForm({
  selection, errors, disabled, showErrors, onChange,
}: DriverOptionsFormProps<'pgbench'>) {
  const [touched, setTouched] = useState<Partial<Record<PgbenchOptionField, boolean>>>({});
  const draft = selection.options;
  const fieldError = (field: PgbenchOptionField) => (showErrors || touched[field] ? errors[field] : undefined);

  return (
    <Stack spacing={3}>
      <Box sx={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: 2 }}>
        {NUMERIC_FIELDS.map(({ field, label, hint }) => {
          const reusesScale = field === 'scale' && !draft.initialize;
          return (
            <TextField
              key={field}
              id={`benchmark-${field}`}
              name={`options.${field}`}
              label={label}
              type="number"
              size="small"
              value={draft[field]}
              disabled={disabled || reusesScale}
              error={Boolean(fieldError(field))}
              helperText={fieldError(field) ?? (reusesScale ? 'Scale is only used when initializing pgbench tables.' : hint)}
              onChange={event => onChange({ driver: selection.driver, options: { ...draft, [field]: event.target.value } })}
              onBlur={() => setTouched(current => ({ ...current, [field]: true }))}
              slotProps={{ htmlInput: { min: 1, step: 1 } }}
            />
          );
        })}
      </Box>
      <Box>
        <FormControlLabel
          control={
            <Checkbox
              id="benchmark-initialize"
              name="options.initialize"
              checked={draft.initialize}
              disabled={disabled}
              onChange={event => onChange({ driver: selection.driver, options: { ...draft, initialize: event.target.checked } })}
            />
          }
          label="Initialize pgbench tables"
        />
        <FormHelperText sx={{ mt: 0 }}>
          {draft.initialize
            ? 'Drops and recreates the pgbench tables for a fresh dataset. Uncheck to reuse existing pgbench tables.'
            : 'Reuses the existing pgbench tables. The run fails if they don\'t exist yet.'}
        </FormHelperText>
      </Box>
    </Stack>
  );
}

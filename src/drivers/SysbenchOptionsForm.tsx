import { useState } from 'react';
import { Box, Checkbox, FormControlLabel, FormHelperText, Stack, TextField, Typography } from '@mui/material';
import type { DriverOptionsFormProps } from './DriverOptionsForm';
import type { SysbenchOptionField } from './sysbench';

const NUMERIC_FIELDS: ReadonlyArray<{ field: SysbenchOptionField; label: string; hint: string }> = [
  { field: 'durationSeconds', label: 'Duration (seconds)', hint: 'How long the measured benchmark runs.' },
  { field: 'threads', label: 'Threads', hint: 'Concurrent workers and database connections.' },
  { field: 'tables', label: 'Tables', hint: 'Number of Sysbench tables to prepare or reuse.' },
  { field: 'tableSize', label: 'Rows per table', hint: 'Rows in each Sysbench table.' },
];

export function SysbenchOptionsForm({
  selection, errors, disabled, showErrors, onChange,
}: DriverOptionsFormProps<'sysbench'>) {
  const [touched, setTouched] = useState<Partial<Record<SysbenchOptionField, boolean>>>({});
  const draft = selection.options;
  const fieldError = (field: SysbenchOptionField) => (showErrors || touched[field] ? errors[field] : undefined);

  return (
    <Stack spacing={3}>
      <Typography variant="body2" color="text.secondary">
        Runs the bundled oltp_read_write workload against MySQL.
      </Typography>
      <Box sx={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: 2 }}>
        {NUMERIC_FIELDS.map(({ field, label, hint }) => (
          <TextField
            key={field}
            id={`benchmark-${field}`}
            name={`options.${field}`}
            label={label}
            type="number"
            size="small"
            value={draft[field]}
            disabled={disabled}
            error={Boolean(fieldError(field))}
            helperText={fieldError(field) ?? hint}
            onChange={event => onChange({ driver: selection.driver, options: { ...draft, [field]: event.target.value } })}
            onBlur={() => setTouched(current => ({ ...current, [field]: true }))}
            slotProps={{ htmlInput: { min: 1, max: 2_147_483_647, step: 1 } }}
          />
        ))}
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
          label="Prepare Sysbench tables"
        />
        <FormHelperText sx={{ mt: 0 }}>
          {draft.initialize
            ? 'Creates benchmark tables in an existing database. Preparation fails if the tables already exist.'
            : 'Reuse existing tables. Table count and size must match the existing dataset.'}
        </FormHelperText>
      </Box>
    </Stack>
  );
}

import { useState } from 'react';
import { Box, Checkbox, FormControlLabel, FormHelperText, Stack, TextField, Typography } from '@mui/material';
import type { DriverOptionsFormProps } from './DriverOptionsForm';
import type { HammerDBOptionField } from './hammerdb';

const NUMERIC_FIELDS: ReadonlyArray<{ field: HammerDBOptionField; label: string; hint: string; min: number }> = [
  { field: 'warehouses', label: 'Warehouses', hint: 'TPROC-C dataset size. Each warehouse adds about 100 MB.', min: 1 },
  { field: 'virtualUsers', label: 'Virtual users', hint: 'Concurrent workers; at most the warehouse count.', min: 1 },
  { field: 'rampupMinutes', label: 'Ramp-up (minutes)', hint: 'Warm-up before measurement starts.', min: 0 },
  { field: 'durationMinutes', label: 'Duration (minutes)', hint: 'How long the measured benchmark runs.', min: 1 },
];

export function HammerDBOptionsForm({
  selection, errors, disabled, showErrors, onChange,
}: DriverOptionsFormProps<'hammerdb'>) {
  const [touched, setTouched] = useState<Partial<Record<HammerDBOptionField, boolean>>>({});
  const draft = selection.options;
  const fieldError = (field: HammerDBOptionField) => (showErrors || touched[field] ? errors[field] : undefined);

  return (
    <Stack spacing={3}>
      <Typography variant="body2" color="text.secondary">
        Runs the HammerDB TPROC-C order-processing workload. It reports new orders per minute (NOPM) and
        transactions per minute (TPM), which are not comparable with pgbench or Sysbench TPS.
      </Typography>
      <Box sx={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: 2 }}>
        {NUMERIC_FIELDS.map(({ field, label, hint, min }) => (
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
            slotProps={{ htmlInput: { min, max: 2_147_483_647, step: 1 } }}
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
          label="Build TPROC-C schema"
        />
        <FormHelperText sx={{ mt: 0 }}>
          {draft.initialize
            ? 'Builds the schema in an existing, empty database before measuring. Schema build time counts against the run timeout.'
            : 'Reuses an existing TPROC-C schema. Warehouses must match the existing dataset.'}
        </FormHelperText>
      </Box>
    </Stack>
  );
}

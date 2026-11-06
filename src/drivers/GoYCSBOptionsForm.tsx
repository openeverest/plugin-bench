import { useState } from 'react';
import { Box, Checkbox, FormControlLabel, FormHelperText, Stack, TextField, Typography } from '@mui/material';
import type { DriverOptionsFormProps } from './DriverOptionsForm';
import type { GoYCSBOptionField } from './goycsb';

const NUMERIC_FIELDS: ReadonlyArray<{ field: Exclude<GoYCSBOptionField, 'collection'>; label: string; hint: string }> = [
  { field: 'recordCount', label: 'Records', hint: 'Dataset size and the key range used for reads and updates.' },
  { field: 'operationCount', label: 'Operations', hint: 'Number of measured operations to perform.' },
  { field: 'threads', label: 'Threads', hint: 'Concurrent workload workers.' },
];

export function GoYCSBOptionsForm({
  selection, errors, disabled, showErrors, onChange,
}: DriverOptionsFormProps<'go-ycsb'>) {
  const [touched, setTouched] = useState<Partial<Record<GoYCSBOptionField, boolean>>>({});
  const draft = selection.options;
  const fieldError = (field: GoYCSBOptionField) => (showErrors || touched[field] ? errors[field] : undefined);

  return (
    <Stack spacing={3}>
      <Typography variant="body2" color="text.secondary">
        Runs Workload A against MongoDB: equal reads and updates.
      </Typography>
      <TextField
        id="benchmark-collection"
        name="options.collection"
        label="Collection"
        size="small"
        fullWidth
        value={draft.collection}
        disabled={disabled}
        error={Boolean(fieldError('collection'))}
        helperText={fieldError('collection') ?? 'Collection in the ycsb database to load or reuse.'}
        onChange={event => onChange({ driver: selection.driver, options: { ...draft, collection: event.target.value } })}
        onBlur={() => setTouched(current => ({ ...current, collection: true }))}
      />
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
          label="Load benchmark data"
        />
        <FormHelperText sx={{ mt: 0 }}>
          {draft.initialize
            ? 'Inserts benchmark records into the selected collection. Use a fresh disposable collection; loading existing keys fails.'
            : 'Reuses existing records. Keep Collection and Records matched to the dataset.'}
        </FormHelperText>
      </Box>
    </Stack>
  );
}

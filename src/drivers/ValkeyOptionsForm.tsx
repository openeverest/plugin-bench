import { useState } from 'react';
import { Box, Stack, TextField, Typography } from '@mui/material';
import type { DriverOptionsFormProps } from './DriverOptionsForm';
import { valkeyFields, type ValkeyOptionsDraft } from './valkey';

export function ValkeyOptionsForm({
  selection, errors, disabled, showErrors, onChange,
}: DriverOptionsFormProps<'valkey'>) {
  const [touched, setTouched] = useState<Partial<Record<keyof ValkeyOptionsDraft, boolean>>>({});
  const draft = selection.options;
  const fieldError = (field: keyof ValkeyOptionsDraft) => (showErrors || touched[field] ? errors[field] : undefined);

  return (
    <Stack spacing={3}>
      <Typography variant="body2" color="text.secondary">
        Seeds benchmark keys, then runs 90% reads and 10% writes.
      </Typography>
      <Box sx={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: 2 }}>
        {valkeyFields.map(({ field, label, max }) => (
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
            helperText={fieldError(field)}
            onChange={event => onChange({ driver: selection.driver, options: { ...draft, [field]: event.target.value } })}
            onBlur={() => setTouched(current => ({ ...current, [field]: true }))}
            slotProps={{ htmlInput: { min: 1, max, step: 1 } }}
          />
        ))}
      </Box>
    </Stack>
  );
}

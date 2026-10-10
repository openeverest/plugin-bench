import { useState } from 'react';
import type { FormEvent } from 'react';
import {
  Box,
  Button,
  Card,
  CardActions,
  CardContent,
  Checkbox,
  FormControlLabel,
  FormHelperText,
  Stack,
  TextField,
  Typography,
} from '@mui/material';
import { BenchmarkTargetSummary } from './BenchmarkTargetSummary.js';
import { initialBenchmarkFormValues, validateBenchmarkForm } from './benchmarkFormValidation.js';
import type { BenchmarkFormField, BenchmarkFormValues } from './benchmarkFormValidation';
import type { BenchmarkTarget } from './benchmarkTarget';
import { createNodeAffinityDraft, prepareNodeAffinityDraft } from './benchmarkNodeAffinity.js';
import type { NodeAffinityDraft } from './benchmarkNodeAffinity';
import { BenchmarkNodeAffinityForm } from './BenchmarkNodeAffinityForm.js';

type NumericField = Exclude<BenchmarkFormField, 'database'>;
type FormState = BenchmarkFormValues & { nodeAffinity: NodeAffinityDraft };

const NUMERIC_FIELDS: ReadonlyArray<{ field: NumericField; label: string; hint: string }> = [
  { field: 'durationSeconds', label: 'Duration (seconds)', hint: 'How long the benchmark runs.' },
  { field: 'clients', label: 'Clients', hint: 'Concurrent database sessions.' },
  { field: 'threads', label: 'Threads', hint: 'Worker threads, up to the number of clients.' },
  { field: 'scale', label: 'Scale', hint: 'Dataset size on initialization (×100,000 rows).' },
];

interface BenchmarkFormProps {
  target: BenchmarkTarget;
  isSubmitting: boolean;
  onSubmit: (values: BenchmarkFormValues) => void | Promise<void>;
}

export function BenchmarkForm({ target, isSubmitting, onSubmit }: BenchmarkFormProps) {
  const [values, setValues] = useState<FormState>(() => ({
    ...initialBenchmarkFormValues, nodeAffinity: createNodeAffinityDraft(),
  }));
  const [touched, setTouched] = useState<Partial<Record<BenchmarkFormField, boolean>>>({});
  const errors = validateBenchmarkForm(values);
  const affinity = prepareNodeAffinityDraft(values.nodeAffinity);
  const canSubmit = Object.keys(errors).length === 0 && affinity.ok && !isSubmitting;

  const updateValue = <K extends keyof FormState>(key: K, value: FormState[K]) => {
    setValues(current => ({ ...current, [key]: value }));
  };
  const markTouched = (field: BenchmarkFormField) => {
    setTouched(current => ({ ...current, [field]: true }));
  };
  const fieldError = (field: BenchmarkFormField) => (touched[field] ? errors[field] : undefined);

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setTouched({ database: true, durationSeconds: true, clients: true, threads: true, scale: true });
    if (canSubmit) void onSubmit(values);
  };

  return (
    <Card variant="outlined" component="form" noValidate onSubmit={submit}>
      <CardContent>
        <Stack spacing={3}>
          <Stack spacing={1.5}>
            <Typography variant="h6" component="h3">
              Run configuration
            </Typography>
            <BenchmarkTargetSummary target={target} />
          </Stack>

          <TextField
            id="benchmark-database"
            name="database"
            label="Database"
            placeholder="Instance default"
            size="small"
            fullWidth
            value={values.database}
            error={Boolean(fieldError('database'))}
            helperText={fieldError('database') ?? "Leave empty to use the instance's default database."}
            onChange={event => updateValue('database', event.target.value)}
            onBlur={() => markTouched('database')}
            slotProps={{ inputLabel: { shrink: true } }}
          />

          <Box sx={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: 2 }}>
            {NUMERIC_FIELDS.map(({ field, label, hint }) => {
              // pgbench takes the scale from existing tables when it doesn't initialize.
              const reusesScale = field === 'scale' && !values.initialize;
              return (
                <TextField
                  key={field}
                  id={`benchmark-${field}`}
                  name={field}
                  label={label}
                  type="number"
                  size="small"
                  value={values[field]}
                  disabled={reusesScale}
                  error={Boolean(fieldError(field))}
                  helperText={fieldError(field) ?? (reusesScale ? 'Scale is only used when initializing pgbench tables.' : hint)}
                  onChange={event => updateValue(field, event.target.value)}
                  onBlur={() => markTouched(field)}
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
                  name="initialize"
                  checked={values.initialize}
                  onChange={event => updateValue('initialize', event.target.checked)}
                />
              }
              label="Initialize pgbench tables"
            />
            <FormHelperText sx={{ mt: 0 }}>
              {values.initialize
                ? 'Drops and recreates the pgbench tables for a fresh dataset. Uncheck to reuse existing pgbench tables.'
                : 'Reuses the existing pgbench tables. The run fails if they don\'t exist yet.'}
            </FormHelperText>
          </Box>
          <BenchmarkNodeAffinityForm draft={values.nodeAffinity}
            errors={'errors' in affinity ? affinity.errors : {}} disabled={isSubmitting}
            onChange={draft => updateValue('nodeAffinity', draft)} />
        </Stack>
      </CardContent>
      <CardActions sx={{ px: 2, pb: 2 }}>
        <Button type="submit" variant="contained" disabled={!canSubmit}>
          {isSubmitting ? 'Starting…' : 'Start benchmark'}
        </Button>
      </CardActions>
    </Card>
  );
}

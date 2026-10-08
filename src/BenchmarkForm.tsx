import { useState } from 'react';
import type { FormEvent } from 'react';
import {
  Accordion,
  AccordionDetails,
  AccordionSummary,
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
import { BenchmarkTargetSummary } from './BenchmarkTargetSummary';
import { initialBenchmarkFormValues, validateBenchmarkForm } from './benchmarkFormValidation';
import type { BenchmarkFormField, BenchmarkFormValues } from './benchmarkFormValidation';
import type { BenchmarkTarget } from './benchmarkTarget';

type NumericField = 'durationSeconds' | 'clients' | 'threads' | 'scale';
type ResourceField = 'cpuRequest' | 'cpuLimit' | 'memoryRequest' | 'memoryLimit';

const NUMERIC_FIELDS: ReadonlyArray<{ field: NumericField; label: string; hint: string }> = [
  { field: 'durationSeconds', label: 'Duration (seconds)', hint: 'How long the benchmark runs.' },
  { field: 'clients', label: 'Clients', hint: 'Concurrent database sessions.' },
  { field: 'threads', label: 'Threads', hint: 'Worker threads, up to the number of clients.' },
  { field: 'scale', label: 'Scale', hint: 'Dataset size on initialization (×100,000 rows).' },
];

const RESOURCE_FIELDS: ReadonlyArray<{ field: ResourceField; label: string; hint: string }> = [
  { field: 'cpuRequest', label: 'CPU request (millicores)', hint: '1000m = 1 CPU core.' },
  { field: 'cpuLimit', label: 'CPU limit (millicores)', hint: '1000m = 1 CPU core.' },
  { field: 'memoryRequest', label: 'Memory request (MiB)', hint: '1024 MiB = 1 GiB.' },
  { field: 'memoryLimit', label: 'Memory limit (MiB)', hint: '1024 MiB = 1 GiB.' },
];

interface BenchmarkFormProps {
  target: BenchmarkTarget;
  isSubmitting: boolean;
  onSubmit: (values: BenchmarkFormValues) => void | Promise<void>;
}

export function BenchmarkForm({ target, isSubmitting, onSubmit }: BenchmarkFormProps) {
  const [values, setValues] = useState<BenchmarkFormValues>({ ...initialBenchmarkFormValues });
  const [touched, setTouched] = useState<Partial<Record<BenchmarkFormField, boolean>>>({});
  const [resourcesExpanded, setResourcesExpanded] = useState(false);
  const errors = validateBenchmarkForm(values);
  const canSubmit = Object.keys(errors).length === 0 && !isSubmitting;
  const resourceError = RESOURCE_FIELDS.some(({ field }) => Boolean(errors[field]));

  const updateValue = <K extends keyof BenchmarkFormValues>(key: K, value: BenchmarkFormValues[K]) => {
    setValues(current => ({ ...current, [key]: value }));
  };
  const markTouched = (field: BenchmarkFormField) => {
    setTouched(current => ({ ...current, [field]: true }));
  };
  const fieldError = (field: BenchmarkFormField) => (touched[field] ? errors[field] : undefined);

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setTouched({ database: true, durationSeconds: true, clients: true, threads: true, scale: true,
      cpuRequest: true, cpuLimit: true, memoryRequest: true, memoryLimit: true });
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

          <Accordion
            expanded={resourcesExpanded}
            onChange={(_, expanded) => setResourcesExpanded(expanded)}
            disableGutters
            sx={{ boxShadow: 'none', border: 1, borderColor: 'divider', borderRadius: 1, '&:before': { display: 'none' } }}
          >
            <AccordionSummary expandIcon={<span aria-hidden="true">⌄</span>}>
              <Stack spacing={0.5}>
                <Typography variant="subtitle1">Job resources</Typography>
                {resourceError && <Typography variant="caption" color="error">Fix the resource fields to start a run.</Typography>}
              </Stack>
            </AccordionSummary>
            <AccordionDetails>
              <Stack spacing={2}>
                <Typography variant="body2" color="text.secondary">
                  CPU and memory for the benchmark workload generator. Leave a field empty to use its deployment default.
                </Typography>
                <Typography variant="body2" color="text.secondary">
                  Chart defaults (unless changed at deployment): CPU request 100m, limit 1000m; memory request 128Mi, limit 512Mi.
                </Typography>
                <Box sx={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: 2 }}>
                  {RESOURCE_FIELDS.map(({ field, label, hint }) => (
                    <TextField
                      key={field}
                      id={`benchmark-${field}`}
                      name={field}
                      label={label}
                      type="number"
                      size="small"
                      value={values[field]}
                      error={Boolean(fieldError(field))}
                      helperText={fieldError(field) ?? hint}
                      onChange={event => updateValue(field, event.target.value)}
                      onBlur={() => markTouched(field)}
                      placeholder="Use deployment default"
                      slotProps={{ htmlInput: { min: 1, step: 1 } }}
                    />
                  ))}
                </Box>
              </Stack>
            </AccordionDetails>
          </Accordion>
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

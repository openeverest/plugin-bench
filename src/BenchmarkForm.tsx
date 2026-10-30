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
  MenuItem,
  Stack,
  TextField,
  Typography,
} from '@mui/material';
import { BenchmarkTargetSummary } from './BenchmarkTargetSummary';
import { createInitialBenchmarkFormValues, isBenchmarkDatabaseRequired, validateBenchmarkForm } from './benchmarkFormValidation';
import type { BenchmarkFormField, BenchmarkFormValues } from './benchmarkFormValidation';
import type { BenchmarkTarget } from './benchmarkTarget';
import { prepareNodeAffinityDraft } from './benchmarkNodeAffinity';
import { BenchmarkNodeAffinityForm } from './BenchmarkNodeAffinityForm';
import { benchmarkDrivers, createDriverDraft, defaultBenchmarkDriver, isBenchmarkDriverId } from './drivers';
import { DriverOptionsForm } from './drivers/DriverOptionsForm';
import type { BenchmarkDriverId, DriverDraft } from './drivers';

type ResourceField = 'cpuRequest' | 'cpuLimit' | 'memoryRequest' | 'memoryLimit';

const RESOURCE_FIELDS: ReadonlyArray<{ field: ResourceField; label: string; hint: string }> = [
  { field: 'cpuRequest', label: 'CPU request (millicores)', hint: '1000m = 1 CPU core.' },
  { field: 'cpuLimit', label: 'CPU limit (millicores)', hint: '1000m = 1 CPU core.' },
  { field: 'memoryRequest', label: 'Memory request (MiB)', hint: '1024 MiB = 1 GiB.' },
  { field: 'memoryLimit', label: 'Memory limit (MiB)', hint: '1024 MiB = 1 GiB.' },
];

interface BenchmarkFormProps {
  target: BenchmarkTarget;
  availableDrivers: readonly BenchmarkDriverId[];
  provider?: string;
  isSubmitting: boolean;
  onSubmit: (values: BenchmarkFormValues) => void | Promise<void>;
}

export function BenchmarkForm({ target, availableDrivers, provider, isSubmitting, onSubmit }: BenchmarkFormProps) {
  const [values, setValues] = useState<BenchmarkFormValues>(
    () => createInitialBenchmarkFormValues(availableDrivers[0] ?? defaultBenchmarkDriver),
  );
  const [touched, setTouched] = useState<Partial<Record<BenchmarkFormField, boolean>>>({});
  const [submitted, setSubmitted] = useState(false);
  const [resourcesExpanded, setResourcesExpanded] = useState(false);
  const errors = validateBenchmarkForm(values, provider);
  const databaseRequired = isBenchmarkDatabaseRequired(values.driver, provider);
  const affinity = prepareNodeAffinityDraft(values.nodeAffinity);
  const driverAvailable = availableDrivers.includes(values.driver);
  const canSubmit = driverAvailable && Object.keys(errors).length === 0 && affinity.ok && !isSubmitting;
  const resourceError = RESOURCE_FIELDS.some(({ field }) => Boolean(errors[field]));

  const updateValue = <K extends BenchmarkFormField | 'nodeAffinity'>(key: K, value: BenchmarkFormValues[K]) => {
    setValues(current => ({ ...current, [key]: value }));
  };
  const markTouched = (field: BenchmarkFormField) => {
    setTouched(current => ({ ...current, [field]: true }));
  };
  const fieldError = (field: BenchmarkFormField) => (touched[field] ? errors[field] : undefined);
  const updateDriver = (selection: DriverDraft) => setValues(current => ({ ...current, ...selection }));

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setSubmitted(true);
    setTouched({ database: true,
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
            placeholder={databaseRequired ? 'Existing database name' : 'Instance default'}
            required={databaseRequired}
            size="small"
            fullWidth
            value={values.database}
            error={Boolean(fieldError('database'))}
            helperText={fieldError('database') ?? (databaseRequired
              ? 'Enter an existing disposable database. This provider does not supply a default; table preparation does not create the database.'
              : "Leave empty to use the instance's default database.")}
            onChange={event => updateValue('database', event.target.value)}
            onBlur={() => markTouched('database')}
            slotProps={{ inputLabel: { shrink: true }, htmlInput: { 'aria-label': 'Database' } }}
          />

          <TextField
            id="benchmark-driver"
            name="driver"
            label="Benchmark driver"
            select
            size="small"
            value={driverAvailable ? values.driver : ''}
            disabled={isSubmitting || availableDrivers.length === 0}
            onChange={event => {
              const driver = event.target.value;
              if (!isSubmitting && isBenchmarkDriverId(driver) && availableDrivers.includes(driver) && driver !== values.driver) {
                updateDriver(createDriverDraft(driver));
                setSubmitted(false);
              }
            }}
          >
            {availableDrivers.map(driver => (
              <MenuItem key={driver} value={driver}>{benchmarkDrivers[driver].label}</MenuItem>
            ))}
          </TextField>
          {driverAvailable && (
            <DriverOptionsForm key={values.driver} selection={values} errors={errors.options ?? {}}
              disabled={isSubmitting} showErrors={submitted} onChange={updateDriver} />
          )}

          <Accordion
            variant="outlined"
            expanded={resourcesExpanded}
            onChange={(_, expanded) => setResourcesExpanded(expanded)}
            disableGutters
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

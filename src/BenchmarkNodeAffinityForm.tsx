import { useId, useState } from 'react';
import {
  Accordion, AccordionDetails, AccordionSummary, Alert, Button, Chip,
  FormControlLabel, Stack, Switch, TextField, Typography,
} from '@mui/material';
import { createNodeLabelRow } from './benchmarkNodeAffinity';
import type { NodeAffinityDraft, NodeAffinityErrors, NodeLabelRow } from './benchmarkNodeAffinity';

interface BenchmarkNodeAffinityFormProps {
  draft: NodeAffinityDraft;
  errors: NodeAffinityErrors;
  disabled?: boolean;
  onChange: (draft: NodeAffinityDraft) => void;
}

export function BenchmarkNodeAffinityForm({ draft, errors, disabled = false, onChange }: BenchmarkNodeAffinityFormProps) {
  const id = useId();
  const [expanded, setExpanded] = useState(false);
  const [touched, setTouched] = useState<ReadonlySet<string>>(new Set());
  const errorCount = Object.keys(errors).length;
  const visibleError = (path: string) => (touched.has(path) ? errors[path] : undefined);
  const markTouched = (path: string) => setTouched(current => new Set(current).add(path));
  const updateRow = (row: NodeLabelRow) =>
    onChange({ ...draft, rows: draft.rows.map(item => (item.id === row.id ? row : item)) });

  return (
    <Accordion variant="outlined" disableGutters expanded={expanded} onChange={(_, open) => setExpanded(open)}>
      <AccordionSummary id={`${id}-summary`} aria-controls={`${id}-details`}
        expandIcon={<Typography aria-hidden="true">⌄</Typography>}>
        <Stack direction="row" spacing={2} sx={{ alignItems: 'center', flexWrap: 'wrap' }}>
          <Typography variant="subtitle1">Job placement</Typography>
          <Chip size="small" label={draft.enabled ? 'Node labels required' : 'Normal scheduling'} />
          {errorCount > 0 && <Chip size="small" color="error" role="status"
            label={`${errorCount} placement ${errorCount === 1 ? 'error' : 'errors'}`} />}
        </Stack>
      </AccordionSummary>
      <AccordionDetails>
        <Stack spacing={2}>
          <FormControlLabel label="Run on nodes with specific labels" control={
            <Switch checked={draft.enabled} disabled={disabled}
              onChange={event => onChange({
                ...draft,
                enabled: event.target.checked,
                rows: event.target.checked && draft.rows.length === 0 ? [createNodeLabelRow()] : draft.rows,
              })} />
          } />
          <Typography variant="body2" color="text.secondary">
            Rules apply to benchmark runner nodes in the cluster hosting this plugin, not the database instance.
            Leave this off to use the deployment&apos;s default placement.
          </Typography>
          {draft.enabled && <>
            <Alert severity="info">
              The runner only starts on a node that has all of these labels. Runs fail if no such node can take it.
            </Alert>
            {errors.nodeAffinity && <Alert severity="error">{errors.nodeAffinity}</Alert>}
            {draft.rows.map((row, index) => (
              <Stack key={row.id} direction={{ xs: 'column', sm: 'row' }} spacing={2} sx={{ alignItems: 'flex-start' }}>
                <TextField id={`${id}-${row.id}-key`} label={`Label key ${index + 1}`} size="small" fullWidth
                  disabled={disabled} value={row.key} placeholder="node-role/benchmark"
                  error={Boolean(visibleError(`${row.id}.key`))} helperText={visibleError(`${row.id}.key`)}
                  onBlur={() => markTouched(`${row.id}.key`)}
                  onChange={event => updateRow({ ...row, key: event.target.value })} />
                <TextField id={`${id}-${row.id}-value`} label={`Label value ${index + 1}`} size="small" fullWidth
                  disabled={disabled} value={row.value} placeholder="true"
                  error={Boolean(visibleError(`${row.id}.value`))} helperText={visibleError(`${row.id}.value`)}
                  onBlur={() => markTouched(`${row.id}.value`)}
                  onChange={event => updateRow({ ...row, value: event.target.value })} />
                <Button type="button" size="small" disabled={disabled} sx={{ flexShrink: 0 }}
                  aria-label={`Remove label ${index + 1}`}
                  onClick={() => onChange({ ...draft, rows: draft.rows.filter(item => item.id !== row.id) })}>
                  Remove
                </Button>
              </Stack>
            ))}
            <Button type="button" size="small" disabled={disabled} sx={{ alignSelf: 'flex-start' }}
              onClick={() => onChange({ ...draft, rows: [...draft.rows, createNodeLabelRow()] })}>
              Add label
            </Button>
          </>}
        </Stack>
      </AccordionDetails>
    </Accordion>
  );
}

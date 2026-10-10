import { useId, useState } from 'react';
import {
  Accordion, AccordionDetails, AccordionSummary, Alert, Box, Button, Chip,
  FormControlLabel, FormHelperText, MenuItem, Stack, Switch, TextField, Typography,
} from '@mui/material';
import type {
  NodeAffinityConditionDraft, NodeAffinityDraft, NodeAffinityErrors,
  NodeAffinityPreferredDraft, NodeAffinityRequiredDraft, NodeSelectorOperator,
} from './benchmarkNodeAffinity';

const OPERATORS: ReadonlyArray<[NodeSelectorOperator, string]> = [
  ['In', 'In (one of)'], ['NotIn', 'NotIn (none of)'], ['Exists', 'Exists'],
  ['DoesNotExist', 'DoesNotExist'], ['Gt', 'Gt (greater than)'], ['Lt', 'Lt (less than)'],
];
const newCondition = (): NodeAffinityConditionDraft => ({ key: '', operator: 'In', values: [''] });

interface ConditionEditorProps {
  condition: NodeAffinityConditionDraft;
  label: string;
  path: string;
  errors: NodeAffinityErrors;
  disabled: boolean;
  onChange: (condition: NodeAffinityConditionDraft) => void;
  onRemove: () => void;
}

function ConditionEditor({ condition, label, path, errors, disabled, onChange, onRemove }: ConditionEditorProps) {
  const id = useId();
  const existence = condition.operator === 'Exists' || condition.operator === 'DoesNotExist';
  const numeric = condition.operator === 'Gt' || condition.operator === 'Lt';
  const changeOperator = (operator: NodeSelectorOperator) => {
    // Existence rules must not retain hidden values. Numeric rules have one threshold.
    const values = operator === 'Exists' || operator === 'DoesNotExist' ? []
      : operator === 'Gt' || operator === 'Lt' ? [condition.values[0] ?? '']
      : condition.values.length > 0 ? condition.values : [''];
    onChange({ ...condition, operator, values });
  };
  return (
    <Box sx={{ borderLeft: 2, borderColor: 'divider', pl: 2 }}>
      <Stack spacing={1.5}>
        <Stack direction="row" sx={{ justifyContent: 'space-between', alignItems: 'center' }}>
          <Typography variant="subtitle2">{label}</Typography>
          <Button type="button" size="small" disabled={disabled} onClick={onRemove}
            aria-label={`Remove ${label.toLowerCase()}`}>
            Remove condition
          </Button>
        </Stack>
        <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2}>
          <TextField id={`${id}-key`} label="Label key" size="small" fullWidth disabled={disabled}
            value={condition.key} placeholder="kubernetes.io/hostname"
            error={Boolean(errors[`${path}.key`])} helperText={errors[`${path}.key`]}
            onChange={event => onChange({ ...condition, key: event.target.value })} />
          <TextField id={`${id}-operator`} label="Operator" size="small" select disabled={disabled}
            sx={{ minWidth: 180 }} value={condition.operator}
            error={Boolean(errors[`${path}.operator`])} helperText={errors[`${path}.operator`]}
            onChange={event => changeOperator(event.target.value as NodeSelectorOperator)}>
            {OPERATORS.map(([value, name]) => <MenuItem key={value} value={value}>{name}</MenuItem>)}
          </TextField>
        </Stack>
        {!existence && (
          <Stack spacing={1}>
            {condition.values.map((value, index) => (
              <Stack key={index} direction="row" spacing={1} sx={{ alignItems: 'flex-start' }}>
                <TextField id={`${id}-value-${index}`} label={numeric ? 'Integer threshold' : `Value ${index + 1}`}
                  size="small" fullWidth disabled={disabled} value={value}
                  slotProps={numeric ? { htmlInput: { inputMode: 'numeric' } } : undefined}
                  error={Boolean(errors[`${path}.values[${index}]`] || errors[`${path}.values`])}
                  helperText={errors[`${path}.values[${index}]`]}
                  onChange={event => onChange({ ...condition,
                    values: condition.values.map((item, i) => i === index ? event.target.value : item),
                  })} />
                {!numeric && (
                  <Button type="button" size="small" disabled={disabled}
                    aria-label={`Remove value ${index + 1} from ${label.toLowerCase()}`}
                    onClick={() => onChange({ ...condition, values: condition.values.filter((_, i) => i !== index) })}>
                    Remove value
                  </Button>
                )}
              </Stack>
            ))}
            {!numeric && <Button type="button" size="small" disabled={disabled} sx={{ alignSelf: 'flex-start' }}
              onClick={() => onChange({ ...condition, values: [...condition.values, ''] })}>
              Add value
            </Button>}
            <FormHelperText error={Boolean(errors[`${path}.values`])}>
              {errors[`${path}.values`] ?? (numeric ? 'Compare a numeric node-label value against this integer.'
                : 'Each entry is an exact label value. Empty entries match an empty label value.')}
            </FormHelperText>
          </Stack>
        )}
        {existence && errors[`${path}.values`] && <FormHelperText error>{errors[`${path}.values`]}</FormHelperText>}
      </Stack>
    </Box>
  );
}

interface GroupEditorProps {
  group: NodeAffinityRequiredDraft | NodeAffinityPreferredDraft;
  kind: 'required' | 'preferred';
  index: number;
  errors: NodeAffinityErrors;
  disabled: boolean;
  onChange: (group: NodeAffinityRequiredDraft | NodeAffinityPreferredDraft) => void;
  onRemove: () => void;
}

function GroupEditor({ group, kind, index, errors, disabled, onChange, onRemove }: GroupEditorProps) {
  const id = useId();
  const path = `${kind}[${index}]`;
  const title = `${kind === 'required' ? 'Required' : 'Preferred'} group ${index + 1}`;
  return (
    <Box component="fieldset" sx={{ m: 0, p: 2, minWidth: 0, border: 1, borderColor: 'divider', borderRadius: 1 }}>
      <Typography component="legend" variant="subtitle2">{title}</Typography>
      <Stack spacing={2}>
        <Button type="button" size="small" disabled={disabled} sx={{ alignSelf: 'flex-end' }}
          aria-label={`Remove ${title.toLowerCase()}`} onClick={onRemove}>Remove group</Button>
        {'weight' in group && <TextField id={`${id}-weight`} label="Weight" size="small" type="number"
          disabled={disabled} value={group.weight} slotProps={{ htmlInput: { min: 1, max: 100, step: 1 } }}
          error={Boolean(errors[`${path}.weight`])}
          helperText={errors[`${path}.weight`] ?? 'Preference weight, from 1 to 100.'}
          onChange={event => onChange({ ...group, weight: event.target.value })} />}
        {errors[path] && <FormHelperText error>{errors[path]}</FormHelperText>}
        {group.matchExpressions.map((condition, conditionIndex) => (
          <ConditionEditor key={conditionIndex} condition={condition}
            label={`${title} condition ${conditionIndex + 1}`} path={`${path}.matchExpressions[${conditionIndex}]`}
            errors={errors} disabled={disabled}
            onChange={next => onChange({ ...group, matchExpressions: group.matchExpressions.map(
              (item, i) => i === conditionIndex ? next : item),
            })}
            onRemove={() => onChange({ ...group,
              matchExpressions: group.matchExpressions.filter((_, i) => i !== conditionIndex),
            })} />
        ))}
        <Button type="button" size="small" disabled={disabled} sx={{ alignSelf: 'flex-start' }}
          onClick={() => onChange({ ...group, matchExpressions: [...group.matchExpressions, newCondition()] })}>
          Add condition
        </Button>
      </Stack>
    </Box>
  );
}

interface BenchmarkNodeAffinityFormProps {
  draft: NodeAffinityDraft;
  errors: NodeAffinityErrors;
  disabled?: boolean;
  onChange: (draft: NodeAffinityDraft) => void;
}

export function BenchmarkNodeAffinityForm({ draft, errors, disabled = false, onChange }: BenchmarkNodeAffinityFormProps) {
  const id = useId();
  const [expanded, setExpanded] = useState(false);
  const errorCount = Object.keys(errors).length;
  return (
    <Accordion variant="outlined" disableGutters expanded={expanded} onChange={(_, open) => setExpanded(open)}>
      <AccordionSummary id={`${id}-summary`} aria-controls={`${id}-details`}
        expandIcon={<Typography aria-hidden="true">⌄</Typography>}>
        <Stack direction="row" spacing={2} sx={{ alignItems: 'center', flexWrap: 'wrap' }}>
          <Typography variant="subtitle1">Job placement</Typography>
          <Chip size="small" label={draft.enabled ? 'Affinity enabled' : 'Normal scheduling'} />
          {errorCount > 0 && <Chip size="small" color="error" role="status"
            label={`${errorCount} placement ${errorCount === 1 ? 'error' : 'errors'}`} />}
        </Stack>
      </AccordionSummary>
      <AccordionDetails>
        <Stack spacing={2}>
          <FormControlLabel label="Configure node affinity" control={
            <Switch checked={draft.enabled} disabled={disabled}
              onChange={event => onChange({ ...draft, enabled: event.target.checked })} />
          } />
          <Typography variant="body2" color="text.secondary">
            Rules apply to benchmark runner nodes in the cluster hosting this plugin, not the database instance.
            Leaving affinity off uses normal Kubernetes scheduling.
          </Typography>
          {draft.enabled && <>
            <Alert severity="info">
              Required rules can keep the Job waiting if no eligible node is available.
              Affinity does not override available resources, taints, or other scheduling constraints.
            </Alert>
            {errors.nodeAffinity && <Alert severity="error">{errors.nodeAffinity}</Alert>}
            <Typography variant="subtitle2" component="h4">Required rules</Typography>
            <Typography variant="body2" color="text.secondary">
              A node must match all conditions (AND) in at least one required group (OR).
            </Typography>
            {draft.required.map((group, index) => (
              <Stack key={index} spacing={1}>
                {index > 0 && <Typography variant="caption">OR</Typography>}
                <GroupEditor group={group} kind="required" index={index} errors={errors} disabled={disabled}
                  onChange={next => onChange({ ...draft,
                    required: draft.required.map((item, i) => i === index ? next : item),
                  })}
                  onRemove={() => onChange({ ...draft, required: draft.required.filter((_, i) => i !== index) })} />
              </Stack>
            ))}
            <Button type="button" size="small" disabled={disabled} sx={{ alignSelf: 'flex-start' }}
              onClick={() => onChange({ ...draft, required: [...draft.required, { matchExpressions: [newCondition()] }] })}>
              Add required group
            </Button>
            <Typography variant="subtitle2" component="h4">Preferred rules</Typography>
            <Typography variant="body2" color="text.secondary">
              A group favors nodes matching all its conditions. Higher weights give a stronger preference,
              but nonmatching eligible nodes may still run the Job.
            </Typography>
            {draft.preferred.map((group, index) => (
              <GroupEditor key={index} group={group} kind="preferred" index={index} errors={errors} disabled={disabled}
                onChange={next => onChange({ ...draft, preferred: draft.preferred.map(
                  (item, i) => i === index ? { ...group, ...next } : item),
                })}
                onRemove={() => onChange({ ...draft, preferred: draft.preferred.filter((_, i) => i !== index) })} />
            ))}
            <Button type="button" size="small" disabled={disabled} sx={{ alignSelf: 'flex-start' }}
              onClick={() => onChange({ ...draft,
                preferred: [...draft.preferred, { weight: '50', matchExpressions: [newCondition()] }],
              })}>Add preferred group</Button>
          </>}
        </Stack>
      </AccordionDetails>
    </Accordion>
  );
}

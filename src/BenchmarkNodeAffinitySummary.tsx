import { Box, Stack, Typography } from '@mui/material';
import type { NodeAffinity, NodeSelectorRequirement, NodeSelectorTerm } from './benchmarkNodeAffinity';

function describeRequirement(requirement: NodeSelectorRequirement, field: boolean): string {
  const prefix = field ? 'Field' : 'Label';
  const values = requirement.values ?? [];
  const suffix = values.length > 0 ? ` ${JSON.stringify(values)}` : '';
  return `${prefix}: ${requirement.key} ${requirement.operator}${suffix}`;
}

function TermSummary({ term }: { term: NodeSelectorTerm }) {
  const conditions = [
    ...(term.matchExpressions ?? []).map(item => describeRequirement(item, false)),
    ...(term.matchFields ?? []).map(item => describeRequirement(item, true)),
  ];
  return (
    <Box component="ul" sx={{ m: 0, pl: 2.5 }}>
      {conditions.map((condition, index) => (
        <Typography key={index} component="li" variant="body2" sx={{ overflowWrap: 'anywhere' }}>
          {condition}
        </Typography>
      ))}
    </Box>
  );
}

export function BenchmarkNodeAffinitySummary({ affinity }: { affinity: NodeAffinity }) {
  const required = affinity.requiredDuringSchedulingIgnoredDuringExecution?.nodeSelectorTerms ?? [];
  const preferred = affinity.preferredDuringSchedulingIgnoredDuringExecution ?? [];
  return (
    <Stack spacing={1}>
      <Typography variant="subtitle2" component="h4">Configured node affinity</Typography>
      {required.length === 0 && preferred.length === 0 ? (
        <Typography variant="body2" color="text.secondary">No node affinity configured.</Typography>
      ) : <>
        <Typography variant="caption" color="text.secondary">
          Saved runner placement rules, not the node ultimately selected by Kubernetes.
        </Typography>
        {required.length > 0 && <>
          <Typography variant="body2">Required: match all conditions in at least one group.</Typography>
          {required.map((term, index) => <Box key={index}>
            <Typography variant="caption">{index > 0 ? 'OR — ' : ''}Required group {index + 1} (AND)</Typography>
            <TermSummary term={term} />
          </Box>)}
        </>}
        {preferred.length > 0 && <>
          <Typography variant="body2">Preferred: weighted preferences, not requirements.</Typography>
          {preferred.map((item, index) => <Box key={index}>
            <Typography variant="caption">Preferred group {index + 1} — weight {item.weight} (AND)</Typography>
            <TermSummary term={item.preference} />
          </Box>)}
        </>}
      </>}
    </Stack>
  );
}

export type NodeSelectorOperator = 'In' | 'NotIn' | 'Exists' | 'DoesNotExist' | 'Gt' | 'Lt';

export type NodeSelectorRequirement = {
  key: string;
  operator: NodeSelectorOperator;
  values?: string[];
};

export type NodeSelectorTerm = {
  matchExpressions?: NodeSelectorRequirement[];
  matchFields?: NodeSelectorRequirement[];
};

export type NodeAffinity = {
  requiredDuringSchedulingIgnoredDuringExecution?: { nodeSelectorTerms: NodeSelectorTerm[] };
  preferredDuringSchedulingIgnoredDuringExecution?: Array<{ weight: number; preference: NodeSelectorTerm }>;
};

// The editor only builds "label key = value" rules; all rows must match (AND).
export type NodeLabelRow = { id: number; key: string; value: string };
export type NodeAffinityDraft = { enabled: boolean; rows: NodeLabelRow[] };
// Keyed by `${row.id}.key` / `${row.id}.value`, or `nodeAffinity` for the whole draft.
export type NodeAffinityErrors = Record<string, string>;
export type PreparedNodeAffinityDraft =
  | { ok: true; nodeAffinity?: NodeAffinity }
  | { ok: false; errors: NodeAffinityErrors };

let nextRowId = 0;

// Stable IDs keep React state attached to the right row when rows are removed.
export function createNodeLabelRow(): NodeLabelRow {
  nextRowId += 1;
  return { id: nextRowId, key: '', value: '' };
}

export function createNodeAffinityDraft(): NodeAffinityDraft {
  return { enabled: false, rows: [] };
}

// Basic checks only; the backend validates Kubernetes label syntax and its
// 400 response is shown to the user.
export function prepareNodeAffinityDraft(draft: NodeAffinityDraft): PreparedNodeAffinityDraft {
  // Preserve disabled drafts in the form, but never send their rules.
  if (!draft.enabled) return { ok: true };
  if (draft.rows.length === 0) {
    return { ok: false, errors: { nodeAffinity: 'Add at least one node label.' } };
  }
  const errors: NodeAffinityErrors = {};
  const seenKeys = new Set<string>();
  for (const row of draft.rows) {
    const key = row.key.trim();
    if (key === '') {
      errors[`${row.id}.key`] = 'Enter a label key.';
    } else if (seenKeys.has(key)) {
      errors[`${row.id}.key`] = 'Each label key can be used once.';
    }
    seenKeys.add(key);
    if (row.value.trim() === '') errors[`${row.id}.value`] = 'Enter a label value.';
  }
  if (Object.keys(errors).length > 0) return { ok: false, errors };
  return { ok: true, nodeAffinity: {
    requiredDuringSchedulingIgnoredDuringExecution: { nodeSelectorTerms: [{
      matchExpressions: draft.rows.map(row => ({ key: row.key.trim(), operator: 'In', values: [row.value.trim()] })),
    }] },
  } };
}

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

const isRequirementList = (value: unknown): boolean =>
  value === undefined || (Array.isArray(value) && value.every(item => isRecord(item) &&
    typeof item.key === 'string' && typeof item.operator === 'string' &&
    (item.values === undefined || (Array.isArray(item.values) && item.values.every(v => typeof v === 'string')))));

const isTerm = (value: unknown): boolean =>
  isRecord(value) && isRequirementList(value.matchExpressions) && isRequirementList(value.matchFields);

// Shape check for status responses so the summary can render them safely.
export function isNodeAffinity(value: unknown): value is NodeAffinity {
  if (!isRecord(value)) return false;
  const required = value.requiredDuringSchedulingIgnoredDuringExecution;
  const preferred = value.preferredDuringSchedulingIgnoredDuringExecution;
  return (required === undefined ||
      (isRecord(required) && Array.isArray(required.nodeSelectorTerms) && required.nodeSelectorTerms.every(isTerm))) &&
    (preferred === undefined || (Array.isArray(preferred) && preferred.every(item =>
      isRecord(item) && typeof item.weight === 'number' && isTerm(item.preference))));
}

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

// The initial editor works with labels. API snapshots may also contain matchFields.
export type NodeAffinityConditionDraft = {
  key: string;
  operator: NodeSelectorOperator;
  values: string[];
};
export type NodeAffinityRequiredDraft = { matchExpressions: NodeAffinityConditionDraft[] };
export type NodeAffinityPreferredDraft = NodeAffinityRequiredDraft & { weight: string };
export type NodeAffinityDraft = {
  enabled: boolean;
  required: NodeAffinityRequiredDraft[];
  preferred: NodeAffinityPreferredDraft[];
};
export type NodeAffinityErrors = Record<string, string>;
export type PreparedNodeAffinityDraft =
  | { ok: true; nodeAffinity?: NodeAffinity }
  | { ok: false; errors: NodeAffinityErrors };

// Return fresh arrays for each form; drafts must not share mutable initial state.
export function createNodeAffinityDraft(): NodeAffinityDraft {
  return { enabled: false, required: [], preferred: [] };
}

const operators: readonly string[] = ['In', 'NotIn', 'Exists', 'DoesNotExist', 'Gt', 'Lt'];
const labelName = /^[A-Za-z0-9](?:[-_.A-Za-z0-9]*[A-Za-z0-9])?$/;
const dnsSubdomain = /^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?(?:\.[a-z0-9](?:[-a-z0-9]*[a-z0-9])?)*$/;

function validLabelValue(value: string): boolean {
  return value === value.trim() && value.length <= 63 && (value === '' || labelName.test(value));
}

function validDnsSubdomain(value: string): boolean {
  return value === value.trim() && value.length <= 253 && dnsSubdomain.test(value);
}

function validLabelKey(key: string): boolean {
  const parts = key.split('/');
  const name = parts[parts.length - 1];
  return key === key.trim() && parts.length <= 2 && name.length > 0 && name.length <= 63 && labelName.test(name) &&
    (parts.length === 1 || validDnsSubdomain(parts[0]));
}

function objectWithFields(
  value: unknown, fields: readonly string[], path: string, errors: NodeAffinityErrors
): value is Record<string, unknown> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    errors[path] = 'Must be an object.';
    return false;
  }
  for (const key of Object.keys(value)) {
    if (!fields.includes(key)) errors[`${path}.${key}`] = 'Unsupported field.';
  }
  return true;
}

function validateRequirement(value: unknown, path: string, field: boolean, errors: NodeAffinityErrors): void {
  if (!objectWithFields(value, ['key', 'operator', 'values'], path, errors)) return;
  const { key, operator, values } = value;
  if (typeof key !== 'string' || (field ? key !== 'metadata.name' : !validLabelKey(key))) {
    errors[`${path}.key`] = field ? 'Only metadata.name is supported.' : 'Enter a valid Kubernetes label key.';
  }
  if (typeof operator !== 'string' || !(field ? ['In', 'NotIn'] : operators).includes(operator)) {
    errors[`${path}.operator`] = 'Unsupported selector operator.';
    return;
  }
  if (values !== undefined && (!Array.isArray(values) || !values.every(item => typeof item === 'string'))) {
    errors[`${path}.values`] = 'Values must be a list of strings.';
    return;
  }
  const items = (values ?? []) as string[];
  if (field) {
    if (items.length !== 1 || !validDnsSubdomain(items[0])) {
      errors[`${path}.values`] = 'Enter exactly one valid node name.';
    }
    return;
  }
  items.forEach((item, index) => {
    if (!validLabelValue(item)) errors[`${path}.values[${index}]`] = 'Enter a valid Kubernetes label value.';
  });
  if ((operator === 'In' || operator === 'NotIn') && items.length === 0) {
    errors[`${path}.values`] = 'Enter at least one value.';
  } else if ((operator === 'Exists' || operator === 'DoesNotExist') && items.length !== 0) {
    errors[`${path}.values`] = 'This operator does not accept values.';
  } else if (operator === 'Gt' || operator === 'Lt') {
    // Label syntax already excludes signed values. BigInt avoids rounding int64 thresholds.
    if (items.length !== 1 || !/^\d+$/.test(items[0]) || BigInt(items[0]) > 9_223_372_036_854_775_807n) {
      errors[`${path}.values`] = 'Enter exactly one integer threshold within the signed 64-bit range.';
    }
  }
}

function validateTerm(value: unknown, path: string, errors: NodeAffinityErrors): void {
  if (!objectWithFields(value, ['matchExpressions', 'matchFields'], path, errors)) return;
  let conditions = 0;
  for (const name of ['matchExpressions', 'matchFields'] as const) {
    const requirements = value[name];
    if (requirements === undefined) continue;
    if (!Array.isArray(requirements)) {
      errors[`${path}.${name}`] = 'Must be a list of conditions.';
      continue;
    }
    conditions += requirements.length;
    requirements.forEach((requirement, index) =>
      validateRequirement(requirement, `${path}.${name}[${index}]`, name === 'matchFields', errors));
  }
  if (conditions === 0) errors[path] = 'Add at least one condition to this group.';
}

// Validate untrusted status JSON before it is used by a renderer. The backend
// remains authoritative for submissions; this does not test node availability.
export function validateNodeAffinity(value: unknown): NodeAffinityErrors {
  const errors: NodeAffinityErrors = {};
  const requiredName = 'requiredDuringSchedulingIgnoredDuringExecution';
  const preferredName = 'preferredDuringSchedulingIgnoredDuringExecution';
  if (!objectWithFields(value, [requiredName, preferredName], 'nodeAffinity', errors)) return errors;
  const required = value[requiredName];
  const requiredPath = `nodeAffinity.${requiredName}`;
  if (required !== undefined && objectWithFields(required, ['nodeSelectorTerms'], requiredPath, errors)) {
    const terms = required.nodeSelectorTerms;
    if (!Array.isArray(terms) || terms.length === 0) {
      errors[`${requiredPath}.nodeSelectorTerms`] = 'Add at least one required group.';
    } else {
      terms.forEach((term, index) => validateTerm(term, `${requiredPath}.nodeSelectorTerms[${index}]`, errors));
    }
  }
  const preferred = value[preferredName];
  const preferredPath = `nodeAffinity.${preferredName}`;
  if (preferred !== undefined) {
    if (!Array.isArray(preferred)) {
      errors[preferredPath] = 'Must be a list of preferred groups.';
    } else {
      preferred.forEach((term, index) => {
        const path = `${preferredPath}[${index}]`;
        if (!objectWithFields(term, ['weight', 'preference'], path, errors)) return;
        if (typeof term.weight !== 'number' || !Number.isInteger(term.weight) || term.weight < 1 || term.weight > 100) {
          errors[`${path}.weight`] = 'Weight must be a whole number from 1 to 100.';
        }
        validateTerm(term.preference, `${path}.preference`, errors);
      });
    }
  }
  return errors;
}

export function isNodeAffinity(value: unknown): value is NodeAffinity {
  return Object.keys(validateNodeAffinity(value)).length === 0;
}

export function prepareNodeAffinityDraft(draft?: NodeAffinityDraft): PreparedNodeAffinityDraft {
  // Preserve disabled drafts in the form, but never send their rules.
  if (!draft?.enabled) return { ok: true };
  if (draft.required.length === 0 && draft.preferred.length === 0) {
    return { ok: false, errors: { nodeAffinity: 'Add at least one required or preferred group.' } };
  }
  const copyTerm = (group: NodeAffinityRequiredDraft): NodeSelectorTerm => ({
    matchExpressions: group.matchExpressions.map(condition => ({
      key: condition.key.trim(), operator: condition.operator, values: [...condition.values],
    })),
  });
  const errors: NodeAffinityErrors = {};
  const affinity: NodeAffinity = {};
  if (draft.required.length > 0) {
    affinity.requiredDuringSchedulingIgnoredDuringExecution = { nodeSelectorTerms: draft.required.map(copyTerm) };
  }
  if (draft.preferred.length > 0) {
    affinity.preferredDuringSchedulingIgnoredDuringExecution = draft.preferred.map((group, index) => {
      if (!/^\d+$/.test(group.weight.trim())) {
        errors[`preferred[${index}].weight`] = 'Weight must be a whole number from 1 to 100.';
      }
      return { weight: Number(group.weight), preference: copyTerm(group) };
    });
  }
  for (const [path, message] of Object.entries(validateNodeAffinity(affinity))) {
    const draftPath = path
      .replace('nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms', 'required')
      .replace('nodeAffinity.preferredDuringSchedulingIgnoredDuringExecution', 'preferred')
      .replace('.preference', '');
    errors[draftPath] = message;
  }
  return Object.keys(errors).length > 0 ? { ok: false, errors } : { ok: true, nodeAffinity: affinity };
}

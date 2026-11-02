// Runners report raw tool precision (pgbench prints six decimals). Values below
// one keep significant digits so small latencies do not round to zero.
export const formatMetricValue = (value: number): string =>
  Math.abs(value) >= 1 || value === 0
    ? value.toLocaleString(undefined, { maximumFractionDigits: 2 })
    : value.toLocaleString(undefined, { maximumSignificantDigits: 3 });

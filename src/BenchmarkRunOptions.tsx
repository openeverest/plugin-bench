import { Fragment } from 'react';
import { Box, Typography } from '@mui/material';

const formatOption = (value: unknown): string =>
  typeof value === 'string' ? value : JSON.stringify(value) ?? String(value);

export function BenchmarkRunOptions({ options }: { options: Record<string, unknown> }) {
  const entries = Object.entries(options);
  return (
    <Box component="section" aria-label="Effective benchmark options">
      <Typography variant="subtitle2" component="h4" gutterBottom>Effective options</Typography>
      {entries.length > 0 ? (
        <Box component="dl" sx={{
          display: 'grid',
          gridTemplateColumns: 'fit-content(45%) minmax(0, 1fr)',
          columnGap: 1.5,
          rowGap: 0.5,
          m: 0,
        }}>
          {entries.map(([key, value]) => (
            <Fragment key={key}>
              <Typography component="dt" variant="body2" color="text.secondary" sx={{ overflowWrap: 'anywhere' }}>
                {key}
              </Typography>
              <Typography component="dd" variant="body2" sx={{ m: 0, overflowWrap: 'anywhere' }}>
                {formatOption(value)}
              </Typography>
            </Fragment>
          ))}
        </Box>
      ) : (
        <Typography variant="body2" color="text.secondary">No driver options recorded.</Typography>
      )}
    </Box>
  );
}

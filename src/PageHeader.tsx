import { Box, Typography } from '@mui/material';

interface PageHeaderProps {
  subtitle: string;
}

export function PageHeader({ subtitle }: PageHeaderProps) {
  return (
    <Box component="header">
      <Typography variant="h5" component="h2">
        Performance Benchmark
      </Typography>
      <Typography variant="body2" color="text.secondary">
        {subtitle}
      </Typography>
    </Box>
  );
}

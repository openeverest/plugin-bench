import { useState } from 'react';
import type { FormEvent } from 'react';
import { Button, Card, CardContent, Stack, TextField, Typography } from '@mui/material';

interface RunLookupProps {
  /** Returns false when the run is already shown. */
  onLookup: (id: string) => boolean;
}

export function RunLookup({ onLookup }: RunLookupProps) {
  const [id, setId] = useState('');
  const [message, setMessage] = useState('');

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const runId = id.trim();
    if (!runId) {
      setMessage('Enter a run ID.');
      return;
    }
    if (!onLookup(runId)) {
      setMessage('This run is already shown.');
      return;
    }
    setMessage('');
    setId('');
  };

  return (
    <Card variant="outlined" component="form" noValidate onSubmit={submit}>
      <CardContent>
        <Stack spacing={1.5}>
          <Typography variant="subtitle1" component="h3">
            Look up a benchmark run
          </Typography>
          <Stack direction={{ xs: 'column', sm: 'row' }} spacing={1} sx={{ alignItems: 'flex-start' }}>
            <TextField
              size="small"
              fullWidth
              label="Run ID"
              value={id}
              error={Boolean(message)}
              helperText={message || undefined}
              onChange={event => setId(event.target.value)}
              slotProps={{ formHelperText: { role: 'alert' } }}
            />
            <Button type="submit" variant="outlined" sx={{ flexShrink: 0 }}>
              Find run
            </Button>
          </Stack>
        </Stack>
      </CardContent>
    </Card>
  );
}

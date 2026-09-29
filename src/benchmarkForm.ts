export type BenchmarkFormValues = {
  database: string;
  durationSeconds: string;
  clients: string;
  threads: string;
  scale: string;
  initialize: boolean;
};

export const initialBenchmarkFormValues: BenchmarkFormValues = {
  database: '',
  durationSeconds: '30',
  clients: '1',
  threads: '1',
  scale: '1',
  initialize: false,
};

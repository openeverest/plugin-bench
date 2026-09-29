import type { CSSProperties } from 'react';
import type { PluginApi } from '@openeverest/plugin-sdk';

const styles: Record<string, CSSProperties> = {
  card: { border: '1px solid #d9d9d9', borderRadius: 8, padding: 20, background: '#fff' },
  heading: { margin: '0 0 16px', fontSize: 18, fontWeight: 600 },
  fieldStack: { display: 'grid', gap: 14 },
  label: { display: 'grid', gap: 6, fontSize: 14, color: '#444' },
  control: { padding: '9px 10px', border: '1px solid #bbb', borderRadius: 4, background: '#f5f5f5' },
  actionRow: { display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' },
  button: { padding: '9px 16px', border: 0, borderRadius: 4, color: '#777', background: '#ddd' },
  badge: { padding: '3px 9px', border: '1px solid #bbb', borderRadius: 12, fontSize: 12, color: '#555' },
  caption: { margin: 0, color: '#666', fontSize: 12 },
};

type BenchmarkFormProps = {
  react: PluginApi['React'];
};

export function BenchmarkForm({ react }: BenchmarkFormProps) {
  return react.createElement(
    'section',
    { style: styles.card },
    react.createElement('h3', { style: styles.heading }, 'Run configuration'),
    react.createElement(
      'div',
      { style: styles.fieldStack },
      react.createElement(
        'label',
        { style: styles.label },
        'Profile',
        react.createElement(
          'select',
          { style: styles.control, value: 'smoke', disabled: true, onChange: () => undefined },
          react.createElement('option', { value: 'smoke' }, 'Smoke test')
        )
      ),
      react.createElement(
        'label',
        { style: styles.label },
        'Duration',
        react.createElement('input', { style: styles.control, value: '30 seconds', disabled: true, readOnly: true })
      ),
      react.createElement(
        'div',
        { style: styles.actionRow },
        react.createElement('button', { type: 'button', style: styles.button, disabled: true }, 'Start benchmark'),
        react.createElement('span', { style: styles.badge }, 'Runner not implemented yet')
      ),
      react.createElement(
        'p',
        { style: styles.caption },
        'The scaffold reserves this workflow for the PostgreSQL benchmark runner.'
      )
    )
  );
}

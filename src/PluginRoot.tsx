import type { ReactNode } from 'react';
import { PluginThemeProvider } from '@openeverest/plugin-theme';

// Emotion cache key: lowercase letters and "-" only, unique across plugins.
const EMOTION_CACHE_KEY = 'plugin-bench';

interface PluginRootProps {
  nonce?: string;
  children: ReactNode;
}

export function PluginRoot({ nonce, children }: PluginRootProps) {
  return (
    <PluginThemeProvider cacheKey={EMOTION_CACHE_KEY} nonce={nonce}>
      {children}
    </PluginThemeProvider>
  );
}

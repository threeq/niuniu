import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';
import path from 'path';

export default defineConfig({
  plugins: [react()],
  test: {
    globals: true,
    environment: 'jsdom',
    clearMocks: true,
    setupFiles: ['./src/test/setup.ts', './src/i18n/test-setup.ts'],
    exclude: [
      'e2e/**',
      'node_modules/**',
      'dist/**',
      '**/*.spec.ts',
      '**/*.spec.tsx',
    ],
    include: ['src/**/*.test.ts', 'src/**/*.test.tsx'],
  },
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
});

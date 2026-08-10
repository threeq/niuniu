import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'
import { defineConfig, globalIgnores } from 'eslint/config'
import noChineseLiteral from './eslint-rules/no-chinese-literal.js'

const localPlugin = { rules: { 'no-chinese-literal': noChineseLiteral } }

export default defineConfig([
  globalIgnores(['dist']),
  {
    files: ['**/*.{ts,tsx}'],
    extends: [
      js.configs.recommended,
      tseslint.configs.recommended,
      reactHooks.configs.flat.recommended,
      reactRefresh.configs.vite,
    ],
    languageOptions: {
      ecmaVersion: 2020,
      globals: globals.browser,
    },
    plugins: { local: localPlugin },
    rules: {
      // Phase D: error (CI hard-gate; bulk migration complete)
      'local/no-chinese-literal': 'error',
      // Standard convention: `_`-prefixed args/vars are intentionally
      // unused (e.g., interface compat, callback signature). Match the
      // ecosystem default which the project's bare config doesn't enable.
      '@typescript-eslint/no-unused-vars': [
        'error',
        {
          argsIgnorePattern: '^_',
          varsIgnorePattern: '^_',
          caughtErrorsIgnorePattern: '^_',
          destructuredArrayIgnorePattern: '^_',
        },
      ],
    },
  },
  {
    // Test files routinely use `any` for mock factories and partial
    // fixtures. Enforcing strict typing there yields no benefit and
    // creates bulk noise; allow it.
    files: ['**/*.test.{ts,tsx}', '**/*.spec.{ts,tsx}', 'e2e/**'],
    rules: { '@typescript-eslint/no-explicit-any': 'off' },
  },
  {
    // shadcn/ui primitives (button, badge, etc.) idiomatically export
    // `xxxVariants` (cva) alongside the component. react-refresh's
    // "components only" rule is incompatible with this pattern but the
    // HMR concern doesn't apply to leaf primitives that rarely change.
    files: ['src/components/ui/**'],
    rules: { 'react-refresh/only-export-components': 'off' },
  },
  {
    files: [
      'src/i18n/**',
      '**/*.test.{ts,tsx}',
      '**/*.spec.{ts,tsx}',
      'e2e/**',
      'eslint-rules/**',
      'scripts/**',
    ],
    rules: { 'local/no-chinese-literal': 'off' },
  },
])

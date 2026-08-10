// Lightweight jest config: ts-jest handles all .ts/.tsx; the test suite
// mocks `react-native` per-file (see __mocks__/react-native.js) instead
// of pulling the full RN runtime through jest, because Jest 30 + the
// pnpm-managed Expo runtime collide on internal `import.meta` polyfills
// (the `__ExpoImportMetaRegistry` "outside of the scope of the test
// code" error).
//
// The mock provides just enough of react-native — View / Text /
// TextInput / StyleSheet / Animated — for component tests using
// @testing-library/react-native + react-test-renderer to render and
// drive the components under test. Tests that need real native module
// behavior (none today; the historical tests are pure logic) can call
// `jest.unmock('react-native')` and arrange their own setup.
module.exports = {
  preset: 'ts-jest',
  testEnvironment: 'node',
  moduleFileExtensions: ['ts', 'tsx', 'js', 'jsx', 'json'],
  setupFiles: ['./jest.setup.js'],
  testMatch: ['**/__tests__/**/*.(test|spec).(ts|tsx|js)', '**/*.(test|spec).(ts|tsx|js)'],
  moduleNameMapper: {
    '^expo-secure-store$': '<rootDir>/__mocks__/expo-secure-store.js',
    '^@react-native-async-storage/async-storage$':
      '<rootDir>/__mocks__/@react-native-async-storage/async-storage.js',
    // Auto-mock react-native for every test. Tests that need the real
    // module can re-require via jest.requireActual.
    '^react-native$': '<rootDir>/__mocks__/react-native.js',
    // @expo/vector-icons ships ESM + native font loading — stub for tests.
    '^@expo/vector-icons/(.+)$': '<rootDir>/__mocks__/@expo/vector-icons/$1.js',
  },
  // @stablelib ships ESM (`"type": "module"` + bare `import` in lib/*.js).
  // ts-jest can't parse those; route .js through babel-jest + babel-preset-expo
  // so ESM → CJS happens via the same chain Metro uses in dev. .ts still goes
  // through ts-jest for type-aware compilation of the rest of the repo.
  //
  // pnpm nests packages under .pnpm/<name>@<ver>/node_modules/<name>, so the
  // ignore pattern must allow the .pnpm/ hop in addition to the flat layout.
  transformIgnorePatterns: [
    'node_modules/(?!(.*(@stablelib|js-base64))/)',
  ],
  transform: {
    // The repo's tsconfig sets jsx: "react-native" so Metro can pass JSX
    // through to the dev bundle, but ts-jest must emit real
    // React.createElement / jsx() calls or Node sees a raw `<` and throws.
    // Override to "react-jsx" (automatic runtime) for tests only.
    '^.+\\.(ts|tsx)$': ['ts-jest', {
      useESM: false,
      tsconfig: { jsx: 'react-jsx' },
    }],
    '^.+\\.jsx?$': 'babel-jest',
  },
};

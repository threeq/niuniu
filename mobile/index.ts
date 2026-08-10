// IMPORTANT: `react-native-get-random-values` installs
// globalThis.crypto.getRandomValues. It MUST be imported before any module
// that (transitively) evaluates `@stablelib/random`, because that module
// captures `defaultRandomSource = new SystemRandomSource()` at import time
// and the source's `isAvailable` check reads `globalThis.crypto` once in
// the constructor. Expo Router eagerly loads the route tree (including
// pair-manual.tsx → src/crypto/identity.ts → @stablelib/x25519/ed25519)
// during `expo-router/entry` evaluation, so the polyfill has to land first.
import 'react-native-get-random-values';
import 'expo-router/entry';

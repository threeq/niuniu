// Mock global fetch
global.fetch = jest.fn();

// React Native expects __DEV__ to be defined globally; Metro injects it in
// the dev bundle but the Node-based jest runtime doesn't.
global.__DEV__ = true;

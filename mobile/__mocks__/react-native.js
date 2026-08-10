// Lightweight react-native mock for component tests.
//
// The real react-native module ships Flow-typed JS that Node can't parse
// without babel-preset-expo, plus a pile of native-bridge initialization
// (`__fbBatchedBridgeConfig`, `__ExpoImportMetaRegistry`, native-modules
// invariants) that crash under jest 30 / pnpm.
//
// For the @testing-library/react-native rendering path we don't need real
// native primitives — react-test-renderer only walks the React tree and
// records prop values. So we expose stub host components named with the
// usual names ('View', 'TextInput', etc.) and a no-op StyleSheet /
// Animated. `getAllByTestId` traverses the rendered tree by `testID`
// prop, which works on these stubs unchanged.
const React = require('react');

function makeHostComponent(name) {
  const Comp = React.forwardRef(function HostStub(props, ref) {
    // react-test-renderer can render arbitrary string-typed elements (it
    // emits them as host nodes in the rendered tree). Returning a string
    // type means @testing-library/react-native's testID query still works
    // because it walks tree nodes by props, regardless of the host type.
    return React.createElement(name, { ...props, ref });
  });
  Comp.displayName = name;
  return Comp;
}

const View = makeHostComponent('View');
const Text = makeHostComponent('Text');
const TextInput = makeHostComponent('TextInput');
const Pressable = makeHostComponent('Pressable');
const ScrollView = makeHostComponent('ScrollView');
const Image = makeHostComponent('Image');

const StyleSheet = {
  create: (styles) => styles,
  flatten: (style) => {
    if (!style) return {};
    if (Array.isArray(style)) {
      return Object.assign({}, ...style.flat().filter(Boolean));
    }
    return style;
  },
  hairlineWidth: 1,
  absoluteFillObject: { position: 'absolute', top: 0, left: 0, right: 0, bottom: 0 },
};

// Minimal Animated stub: tests that don't rely on tween timing pass values
// through synchronously. Components that read .start callbacks invoke them
// immediately so onComplete-style callbacks still fire in tests.
const Animated = {
  View: makeHostComponent('Animated.View'),
  Text: makeHostComponent('Animated.Text'),
  Value: class AnimatedValue {
    constructor(v) {
      this._v = v;
    }
    setValue(v) {
      this._v = v;
    }
    interpolate() {
      return this;
    }
  },
  timing: (_value, _config) => ({
    start: (cb) => cb && cb({ finished: true }),
    stop: () => {},
  }),
  spring: (_value, _config) => ({
    start: (cb) => cb && cb({ finished: true }),
    stop: () => {},
  }),
  parallel: (animations) => ({
    start: (cb) => cb && cb({ finished: true }),
  }),
  sequence: (animations) => ({
    start: (cb) => cb && cb({ finished: true }),
  }),
};

const Platform = {
  OS: 'ios',
  select: (obj) => (obj.ios !== undefined ? obj.ios : obj.default),
  Version: 14,
};

// useColorScheme is read from theme/useTheme. Default to 'light'.
function useColorScheme() {
  return 'light';
}

const KeyboardAvoidingView = makeHostComponent('KeyboardAvoidingView');
const ActivityIndicator = makeHostComponent('ActivityIndicator');
const SafeAreaView = makeHostComponent('SafeAreaView');

const Dimensions = {
  get: () => ({ width: 375, height: 812, scale: 2, fontScale: 1 }),
  addEventListener: () => ({ remove: () => {} }),
};

const Keyboard = {
  dismiss: () => {},
  addListener: () => ({ remove: () => {} }),
};

module.exports = {
  View,
  Text,
  TextInput,
  Pressable,
  ScrollView,
  Image,
  StyleSheet,
  Animated,
  Platform,
  useColorScheme,
  KeyboardAvoidingView,
  ActivityIndicator,
  SafeAreaView,
  Dimensions,
  Keyboard,
};

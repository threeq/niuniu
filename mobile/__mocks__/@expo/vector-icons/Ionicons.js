// Lightweight stub for @expo/vector-icons/Ionicons used in Jest tests.
// The real Ionicons depends on native font loading which doesn't work in Node.
const React = require('react');

const Ionicons = React.forwardRef(function Ionicons({ name, size, color, style, testID }, ref) {
  return React.createElement('Ionicons', { name, size, color, style, testID, ref });
});
Ionicons.displayName = 'Ionicons';

module.exports = Ionicons;
module.exports.default = Ionicons;

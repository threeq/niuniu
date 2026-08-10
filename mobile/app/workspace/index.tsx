import React from 'react';
import { View, Text, StyleSheet } from 'react-native';
import { useThemeColors } from '../../src/theme/useTheme';
import { typography } from '../../src/theme/tokens';

export default function WorkspaceListScreen() {
  const colors = useThemeColors();
  return (
    <View style={[styles.container, { backgroundColor: colors.bg.base }]}>
      <Text style={[typography.pageTitle, { color: colors.text.primary }]}>Workspaces</Text>
      <Text style={{ color: colors.text.secondary, marginTop: 8 }}>Workspace list — to be implemented</Text>
    </View>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1, padding: 20, paddingTop: 60 },
});

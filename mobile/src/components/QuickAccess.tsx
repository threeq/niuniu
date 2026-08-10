import React from 'react';
import { View, Text, Pressable, StyleSheet } from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { useThemeColors } from '../theme/useTheme';
import { spacing } from '../theme/tokens';

interface QuickAccessProps {
  onWorkspaces: () => void;
  onRepos: () => void;
}

export function QuickAccess({ onWorkspaces, onRepos }: QuickAccessProps) {
  const colors = useThemeColors();
  const btnStyle = [styles.button, { backgroundColor: colors.bg.surface, borderColor: colors.border.default }];

  return (
    <View style={styles.container}>
      <Pressable style={btnStyle} onPress={onWorkspaces}>
        <Ionicons name="cube-outline" size={16} color={colors.text.secondary} />
        <Text style={[styles.label, { color: colors.text.primary }]}>Workspaces</Text>
      </Pressable>
      <Pressable style={btnStyle} onPress={onRepos}>
        <Ionicons name="git-branch-outline" size={16} color={colors.text.secondary} />
        <Text style={[styles.label, { color: colors.text.primary }]}>Repos</Text>
      </Pressable>
    </View>
  );
}

const styles = StyleSheet.create({
  container: {
    flexDirection: 'row',
    gap: spacing.sm,
  },
  button: {
    flex: 1,
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.sm,
    borderWidth: 1,
    borderRadius: 10,
    padding: spacing.md,
  },
  label: {
    fontSize: 13,
    fontWeight: '500',
  },
});

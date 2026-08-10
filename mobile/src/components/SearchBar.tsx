import React from 'react';
import { TextInput, View, StyleSheet } from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { useThemeColors } from '../theme/useTheme';
import { typography, spacing, radius } from '../theme/tokens';

export interface SearchBarProps {
  visible: boolean;
  value: string;
  onChange: (v: string) => void;
  placeholder: string;
}

/**
 * 受控搜索条。`visible === false` 时不渲染（不占空间）；为 true 时挂载并 autoFocus。
 *
 * 状态归调用方：toggle 控制由父组件持有 `showSearch` 与 `search`，关闭时清空 query
 * 等业务规则在父组件实现，本组件只负责展示与输入。
 */
export function SearchBar({ visible, value, onChange, placeholder }: SearchBarProps) {
  const colors = useThemeColors();

  if (!visible) return null;

  return (
    <View
      style={[
        styles.wrap,
        { backgroundColor: colors.bg.surface, borderColor: colors.border.default },
      ]}
    >
      <Ionicons
        name="search-outline"
        size={16}
        color={colors.text.tertiary}
        style={styles.icon}
      />
      <TextInput
        autoFocus
        style={[styles.input, { color: colors.text.primary }]}
        placeholder={placeholder}
        placeholderTextColor={colors.text.tertiary}
        value={value}
        onChangeText={onChange}
        autoCapitalize="none"
        clearButtonMode="while-editing"
      />
    </View>
  );
}

const styles = StyleSheet.create({
  wrap: {
    flexDirection: 'row',
    alignItems: 'center',
    marginHorizontal: spacing.lg,
    marginBottom: spacing.md,
    borderRadius: radius.md,
    borderWidth: 1,
    paddingHorizontal: spacing.md,
    height: 40,
  },
  icon: {
    marginRight: spacing.sm,
  },
  input: {
    flex: 1,
    ...typography.body,
    padding: 0,
  },
});

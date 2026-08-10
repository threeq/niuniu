import React from 'react';
import { ScrollView, Pressable, Text, StyleSheet } from 'react-native';
import { useThemeColors } from '../theme/useTheme';
import { spacing } from '../theme/tokens';

interface FilterChipsProps {
  options: { label: string; value: string }[];
  selected: string;
  onSelect: (value: string) => void;
}

export function FilterChips({ options, selected, onSelect }: FilterChipsProps) {
  const colors = useThemeColors();

  return (
    <ScrollView horizontal showsHorizontalScrollIndicator={false} style={styles.container}>
      {options.map((opt) => {
        const isActive = opt.value === selected;
        return (
          <Pressable
            key={opt.value}
            style={[
              styles.chip,
              { backgroundColor: isActive ? colors.text.primary : colors.bg.muted },
            ]}
            onPress={() => onSelect(opt.value)}
          >
            <Text style={[
              styles.chipText,
              { color: isActive ? colors.bg.base : colors.text.secondary },
              isActive && { fontWeight: '600' },
            ]}>
              {opt.label}
            </Text>
          </Pressable>
        );
      })}
    </ScrollView>
  );
}

const styles = StyleSheet.create({
  container: { paddingHorizontal: spacing.lg },
  chip: { paddingHorizontal: 14, paddingVertical: 5, borderRadius: 9999, marginRight: spacing.sm },
  chipText: { fontSize: 12, fontWeight: '500' },
});

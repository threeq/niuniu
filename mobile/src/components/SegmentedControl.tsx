import React from 'react';
import { View, Text, Pressable, StyleSheet } from 'react-native';
import { useThemeColors } from '../theme/useTheme';
import { spacing, radius } from '../theme/tokens';

interface SegmentedControlProps {
  options: string[];
  selected: number;
  onSelect: (index: number) => void;
}

export function SegmentedControl({ options, selected, onSelect }: SegmentedControlProps) {
  const colors = useThemeColors();

  return (
    <View style={[styles.container, { backgroundColor: colors.bg.muted }]}>
      {options.map((option, index) => (
        <Pressable
          key={option}
          onPress={() => onSelect(index)}
          style={[
            styles.option,
            index === selected && [styles.optionActive, { backgroundColor: colors.bg.base }],
          ]}
        >
          <Text style={[
            styles.label,
            { color: index === selected ? colors.text.primary : colors.text.secondary },
            index === selected && { fontWeight: '600' },
          ]}>
            {option}
          </Text>
        </Pressable>
      ))}
    </View>
  );
}

const styles = StyleSheet.create({
  container: {
    flexDirection: 'row',
    borderRadius: radius.sm,
    padding: 2,
  },
  option: {
    paddingVertical: 4,
    paddingHorizontal: spacing.md,
    borderRadius: 6,
  },
  optionActive: {
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.06,
    shadowRadius: 2,
    elevation: 1,
  },
  label: {
    fontSize: 12,
    fontWeight: '500',
  },
});

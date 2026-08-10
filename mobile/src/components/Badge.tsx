import React from 'react';
import { View, Text, StyleSheet } from 'react-native';
import { radius } from '../theme/tokens';

interface BadgeProps {
  label: string;
  color: string;
  bgColor: string;
}

export function Badge({ label, color, bgColor }: BadgeProps) {
  return (
    <View style={[styles.badge, { backgroundColor: bgColor }]}>
      <Text style={[styles.text, { color }]}>{label}</Text>
    </View>
  );
}

const styles = StyleSheet.create({
  badge: {
    paddingHorizontal: 8,
    paddingVertical: 2,
    borderRadius: radius.xs,
  },
  text: {
    fontSize: 11,
    fontWeight: '600',
  },
});

import React, { Component, ReactNode } from 'react';
import { View, Text, TouchableOpacity, StyleSheet } from 'react-native';
import { lightColors, darkColors, typography, spacing, radius } from '../theme/tokens';
import { Appearance } from 'react-native';

interface Props { children: ReactNode }
interface State { hasError: boolean; error: string; }

function getColors() {
  return Appearance.getColorScheme() === 'dark' ? darkColors : lightColors;
}

export class ErrorBoundary extends Component<Props, State> {
  state: State = { hasError: false, error: '' };

  static getDerivedStateFromError(error: Error): State {
    return { hasError: true, error: error.message };
  }

  handleReset = () => this.setState({ hasError: false, error: '' });

  render() {
    if (this.state.hasError) {
      const colors = getColors();
      return (
        <View style={[styles.container, { backgroundColor: colors.bg.base }]}>
          <Text style={[styles.title, { color: colors.text.primary }]}>Something went wrong</Text>
          <Text style={[styles.message, { color: colors.text.secondary }]}>{this.state.error}</Text>
          <TouchableOpacity style={[styles.button, { backgroundColor: colors.brand.accent }]} onPress={this.handleReset}>
            <Text style={styles.buttonText}>Retry</Text>
          </TouchableOpacity>
        </View>
      );
    }
    return this.props.children;
  }
}

const styles = StyleSheet.create({
  container: { flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing.xl },
  title: { ...typography.sectionHead, marginBottom: spacing.sm },
  message: { ...typography.body, textAlign: 'center', marginBottom: spacing.xl },
  button: { paddingHorizontal: spacing.xl, paddingVertical: spacing.md, borderRadius: radius.sm },
  buttonText: { ...typography.bodyMedium, color: '#fff' },
});

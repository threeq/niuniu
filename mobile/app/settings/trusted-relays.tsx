/**
 * trusted-relays.tsx — settings screen for managing the trusted relay origins
 * allowlist.
 *
 * Lists all trusted origins and allows swipe-to-remove (via long-press confirm
 * dialog on non-iOS, or a delete button). New origins can be added by typing
 * a hostname in the input.
 */

import { useState } from 'react';
import {
  Alert,
  FlatList,
  Pressable,
  StyleSheet,
  Text,
  TextInput,
  View,
} from 'react-native';
import { useTranslation } from 'react-i18next';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { useTrustedRelaysStore } from '../../src/stores/trustedRelaysStore';
import { useThemeColors } from '../../src/theme/useTheme';
import { radius, spacing, typography } from '../../src/theme/tokens';

export default function TrustedRelaysScreen() {
  const { t } = useTranslation();
  const colors = useThemeColors();
  const insets = useSafeAreaInsets();

  const origins = useTrustedRelaysStore((s) => s.origins);
  const addOrigin = useTrustedRelaysStore((s) => s.addOrigin);
  const removeOrigin = useTrustedRelaysStore((s) => s.removeOrigin);

  const [newHost, setNewHost] = useState('');

  const handleAdd = () => {
    const trimmed = newHost.trim().toLowerCase();
    if (!trimmed) return;
    addOrigin(trimmed);
    setNewHost('');
  };

  const handleRemove = (host: string) => {
    Alert.alert(
      t('trustedRelays.removeConfirm'),
      host,
      [
        { text: t('pairing.scan.cancelBtn'), style: 'cancel' },
        {
          text: t('trustedRelays.removeConfirm'),
          style: 'destructive',
          onPress: () => removeOrigin(host),
        },
      ],
    );
  };

  return (
    <View
      style={[
        styles.screen,
        { backgroundColor: colors.bg.base, paddingTop: insets.top + spacing.xl },
      ]}
    >
      <Text style={[styles.title, { color: colors.text.primary }]}>
        {t('trustedRelays.title')}
      </Text>

      <FlatList
        data={origins}
        keyExtractor={(item) => item}
        style={styles.list}
        renderItem={({ item }) => (
          <View
            style={[
              styles.row,
              {
                backgroundColor: colors.bg.surface,
                borderColor: colors.border.default,
              },
            ]}
          >
            <Text
              style={[styles.rowText, { color: colors.text.primary }]}
              numberOfLines={1}
            >
              {item}
            </Text>
            <Pressable
              onPress={() => handleRemove(item)}
              hitSlop={8}
              accessibilityRole="button"
              accessibilityLabel={`Remove ${item}`}
            >
              <Text style={[styles.removeBtn, { color: colors.status.error }]}>
                {'Remove'}
              </Text>
            </Pressable>
          </View>
        )}
        ListEmptyComponent={
          <Text style={[styles.empty, { color: colors.text.tertiary }]}>
            {'No trusted relay origins.'}
          </Text>
        }
        contentContainerStyle={styles.listContent}
      />

      {/* Add new origin */}
      <View
        style={[
          styles.addRow,
          {
            borderTopColor: colors.border.default,
            paddingBottom: insets.bottom + spacing.lg,
          },
        ]}
      >
        <TextInput
          style={[
            styles.input,
            {
              backgroundColor: colors.bg.surface,
              borderColor: colors.border.default,
              color: colors.text.primary,
            },
          ]}
          value={newHost}
          onChangeText={setNewHost}
          placeholder="relay.example.com"
          placeholderTextColor={colors.text.tertiary}
          autoCapitalize="none"
          autoCorrect={false}
          returnKeyType="done"
          onSubmitEditing={handleAdd}
        />
        <Pressable
          style={[
            styles.addBtn,
            { backgroundColor: colors.brand.accent },
            !newHost.trim() && { opacity: 0.5 },
          ]}
          onPress={handleAdd}
          disabled={!newHost.trim()}
        >
          <Text style={styles.addBtnText}>{'Add'}</Text>
        </Pressable>
      </View>
    </View>
  );
}

const styles = StyleSheet.create({
  screen: {
    flex: 1,
  },
  title: {
    ...typography.pageTitle,
    paddingHorizontal: spacing.xl,
    marginBottom: spacing.lg,
  },
  list: {
    flex: 1,
  },
  listContent: {
    paddingHorizontal: spacing.xl,
    gap: spacing.xs,
  },
  row: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    padding: spacing.lg,
    borderRadius: radius.sm,
    borderWidth: 1,
    marginBottom: spacing.xs,
  },
  rowText: {
    ...typography.body,
    flex: 1,
  },
  removeBtn: {
    ...typography.label,
    marginLeft: spacing.md,
  },
  empty: {
    ...typography.body,
    textAlign: 'center',
    marginTop: spacing.xl,
  },
  addRow: {
    flexDirection: 'row',
    gap: spacing.sm,
    padding: spacing.xl,
    borderTopWidth: 1,
  },
  input: {
    flex: 1,
    borderWidth: 1,
    borderRadius: radius.sm,
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.sm + 2,
    ...typography.body,
  },
  addBtn: {
    borderRadius: radius.sm,
    paddingHorizontal: spacing.lg,
    paddingVertical: spacing.sm + 2,
    justifyContent: 'center',
  },
  addBtnText: {
    ...typography.bodyMedium,
    color: '#FFFFFF',
  },
});

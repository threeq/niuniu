/**
 * ActiveDesktopSwitcher — a pill/dropdown that lets the user switch which
 * paired desktop is the active connection target.
 *
 * Mount on any (tabs) screen that needs the switcher. For now it is mounted
 * on the dashboard header. Other tabs screens should add it to their own
 * header row when they also need desktop-scoped actions.
 */

import React, { useState } from 'react';
import {
  Modal,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  View,
} from 'react-native';
import { useTranslation } from 'react-i18next';
import { useTransportStore } from '../stores/transportStore';
import { usePairedDesktopsStore } from '../stores/pairedDesktopsStore';
import { useThemeColors } from '../theme/useTheme';
import { radius, spacing, typography } from '../theme/tokens';

export function ActiveDesktopSwitcher() {
  const { t } = useTranslation();
  const colors = useThemeColors();

  const activeDesktopId = useTransportStore((s) => s.activeDesktopId);
  const setActiveDesktopId = useTransportStore((s) => s.setActiveDesktopId);
  const removeCipher = useTransportStore((s) => s.removeCipher);
  const desktops = usePairedDesktopsStore((s) => s.desktops);

  const [modalVisible, setModalVisible] = useState(false);

  const activeDesktop = desktops.find((d) => d.desktopId === activeDesktopId);
  const pillLabel = activeDesktop
    ? activeDesktop.desktopName
    : t('desktops.switcher.noDesktop');

  const handleSelect = (newId: string) => {
    // Clear the cached cipher for the previous desktop before switching
    if (activeDesktopId && activeDesktopId !== newId) {
      removeCipher(activeDesktopId);
    }
    setActiveDesktopId(newId);
    setModalVisible(false);
  };

  return (
    <>
      <Pressable
        onPress={() => setModalVisible(true)}
        style={[
          styles.pill,
          {
            backgroundColor: colors.bg.surface,
            borderColor: colors.border.default,
          },
        ]}
        accessibilityRole="button"
        accessibilityLabel={t('desktops.switcher.active', { name: pillLabel })}
      >
        <View style={styles.pillDot} />
        <Text
          style={[styles.pillText, { color: colors.text.primary }]}
          numberOfLines={1}
        >
          {pillLabel}
        </Text>
        <Text style={[styles.pillChevron, { color: colors.text.tertiary }]}>
          {'›'}
        </Text>
      </Pressable>

      <Modal
        visible={modalVisible}
        transparent
        animationType="slide"
        onRequestClose={() => setModalVisible(false)}
      >
        <Pressable
          style={styles.backdrop}
          onPress={() => setModalVisible(false)}
        >
          <Pressable
            style={[styles.sheet, { backgroundColor: colors.bg.surface }]}
            // Prevent tap-through to backdrop
            onPress={(e) => e.stopPropagation()}
          >
            <Text style={[styles.sheetTitle, { color: colors.text.primary }]}>
              {t('desktops.switcher.title')}
            </Text>

            <ScrollView style={styles.list} bounces={false}>
              {/* Paired desktops */}
              {desktops.map((d) => (
                <Pressable
                  key={d.desktopId}
                  style={[
                    styles.option,
                    {
                      borderColor: colors.border.subtle,
                      backgroundColor:
                        activeDesktopId === d.desktopId
                          ? colors.brand.accentLight
                          : 'transparent',
                    },
                  ]}
                  onPress={() => handleSelect(d.desktopId)}
                >
                  <Text
                    style={[
                      styles.optionText,
                      {
                        color:
                          activeDesktopId === d.desktopId
                            ? colors.brand.accent
                            : colors.text.primary,
                      },
                    ]}
                    numberOfLines={1}
                  >
                    {d.desktopName}
                  </Text>
                  {activeDesktopId === d.desktopId && (
                    <Text style={{ color: colors.brand.accent }}>{'✓'}</Text>
                  )}
                </Pressable>
              ))}
            </ScrollView>
          </Pressable>
        </Pressable>
      </Modal>
    </>
  );
}

const styles = StyleSheet.create({
  pill: {
    flexDirection: 'row',
    alignItems: 'center',
    borderRadius: radius.full,
    borderWidth: 1,
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.xs + 2,
    gap: spacing.xs,
    maxWidth: 128,
  },
  pillDot: {
    width: 6,
    height: 6,
    borderRadius: 3,
    backgroundColor: '#16A34A',
  },
  pillText: {
    ...typography.label,
    flex: 1,
  },
  pillChevron: {
    fontSize: 14,
    fontWeight: '600',
  },
  backdrop: {
    flex: 1,
    backgroundColor: 'rgba(0,0,0,0.4)',
    justifyContent: 'flex-end',
  },
  sheet: {
    borderTopLeftRadius: radius.lg,
    borderTopRightRadius: radius.lg,
    paddingTop: spacing.xl,
    paddingBottom: 40,
    paddingHorizontal: spacing.xl,
    maxHeight: '70%',
  },
  sheetTitle: {
    ...typography.sectionHead,
    marginBottom: spacing.lg,
  },
  list: {
    flexGrow: 0,
  },
  option: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingVertical: spacing.lg,
    paddingHorizontal: spacing.md,
    borderRadius: radius.sm,
    borderBottomWidth: 1,
    marginBottom: spacing.xs,
  },
  optionText: {
    ...typography.bodyMedium,
    flex: 1,
  },
});

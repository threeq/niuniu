import React, {
  forwardRef,
  useCallback,
  useMemo,
  useState,
} from 'react';
import {
  ActivityIndicator,
  Alert,
  Pressable,
  StyleSheet,
  Text,
  TextInput,
  TouchableOpacity,
  View,
} from 'react-native';
import {
  BottomSheetModal,
  BottomSheetView,
  BottomSheetBackdrop,
} from '@gorhom/bottom-sheet';
import type { BottomSheetBackdropProps } from '@gorhom/bottom-sheet';
import { api } from '../api/client';
import type { Column } from '../api/types';
import { useThemeColors } from '../theme/useTheme';
import { radius, spacing, typography } from '../theme/tokens';

// ─── Constants ───────────────────────────────────────────────────────────────

// Backend semantic: 0=low, 1=medium, 2=high, 3=critical (matches web SPA).
// `colorKey` is the theme color slot, kept on the P1..P4 visual scale.
const PRIORITY_OPTIONS = [
  { label: 'P1 紧急', value: 3, colorKey: 'p1' },
  { label: 'P2 高',   value: 2, colorKey: 'p2' },
  { label: 'P3 中',   value: 1, colorKey: 'p3' },
  { label: 'P4 低',   value: 0, colorKey: 'p4' },
] as const;

const DEFAULT_PRIORITY = 1; // medium — matches web SPA's createIssueDialog default

// ─── Props ───────────────────────────────────────────────────────────────────

interface CreateIssueSheetProps {
  columns: Column[];
  onCreated: () => void;
}

// ─── Component ───────────────────────────────────────────────────────────────

export const CreateIssueSheet = forwardRef<BottomSheetModal, CreateIssueSheetProps>(
  function CreateIssueSheet({ columns, onCreated }, ref) {
    const colors = useThemeColors();

    // ── Form state ───────────────────────────────────────────────────────────
    const [title, setTitle] = useState('');
    const [description, setDescription] = useState('');
    const [selectedColumnId, setSelectedColumnId] = useState<number | null>(
      columns.length > 0 ? columns[0].id : null,
    );
    const [priority, setPriority] = useState<number>(DEFAULT_PRIORITY);
    const [submitting, setSubmitting] = useState(false);

    // ── Snap points ──────────────────────────────────────────────────────────
    const snapPoints = useMemo(() => ['70%'], []);

    // ── Backdrop ─────────────────────────────────────────────────────────────
    const renderBackdrop = useCallback(
      (props: BottomSheetBackdropProps) => (
        <BottomSheetBackdrop
          {...props}
          disappearsOnIndex={-1}
          appearsOnIndex={0}
          opacity={0.5}
        />
      ),
      [],
    );

    // ── Reset form on open ───────────────────────────────────────────────────
    const handleSheetChange = useCallback(
      (index: number) => {
        if (index === 0) {
          // sheet opened — reset to defaults
          setTitle('');
          setDescription('');
          setSelectedColumnId(columns.length > 0 ? columns[0].id : null);
          setPriority(DEFAULT_PRIORITY);
        }
      },
      [columns],
    );

    // ── Submit ───────────────────────────────────────────────────────────────
    const handleSubmit = async () => {
      if (!title.trim()) {
        Alert.alert('请填写标题', '标题为必填项');
        return;
      }
      if (submitting) return;

      const columnId = selectedColumnId ?? columns[0]?.id;
      if (!columnId) {
        Alert.alert('无可用列', '项目中暂无看板列，请先添加列');
        return;
      }

      setSubmitting(true);
      try {
        // Backend route is /api/columns/:id/issues; column id is in the path,
        // not in the body. Mirrors web SPA's createIssueDialog.
        await api.post(`/columns/${columnId}/issues`, {
          title: title.trim(),
          description: description.trim() || undefined,
          priority,
        });
        // Dismiss and notify parent
        (ref as React.RefObject<BottomSheetModal>)?.current?.dismiss();
        onCreated();
      } catch (err) {
        console.warn('CreateIssueSheet submit error:', err);
        Alert.alert('创建失败', '无法创建 Issue，请稍后重试');
      } finally {
        setSubmitting(false);
      }
    };

    // ── Render ───────────────────────────────────────────────────────────────
    return (
      <BottomSheetModal
        ref={ref}
        index={0}
        snapPoints={snapPoints}
        backdropComponent={renderBackdrop}
        onChange={handleSheetChange}
        enablePanDownToClose
        backgroundStyle={{ backgroundColor: colors.bg.surface, borderRadius: 20 }}
        handleIndicatorStyle={{ backgroundColor: colors.border.default, width: 36 }}
      >
        <BottomSheetView style={styles.container}>
          {/* Header */}
          <Text style={[styles.header, { color: colors.text.primary }]}>
            创建 Issue
          </Text>

          {/* 标题 */}
          <View style={styles.fieldGroup}>
            <Text style={[styles.label, { color: colors.text.secondary }]}>
              标题 <Text style={{ color: colors.status.error }}>*</Text>
            </Text>
            <TextInput
              style={[
                styles.input,
                {
                  backgroundColor: colors.bg.muted,
                  color: colors.text.primary,
                  borderColor: colors.border.default,
                },
              ]}
              placeholder="输入 Issue 标题..."
              placeholderTextColor={colors.text.tertiary}
              value={title}
              onChangeText={setTitle}
              returnKeyType="next"
              maxLength={200}
            />
          </View>

          {/* 描述 */}
          <View style={styles.fieldGroup}>
            <Text style={[styles.label, { color: colors.text.secondary }]}>描述</Text>
            <TextInput
              style={[
                styles.input,
                styles.textArea,
                {
                  backgroundColor: colors.bg.muted,
                  color: colors.text.primary,
                  borderColor: colors.border.default,
                },
              ]}
              placeholder="（可选）输入描述..."
              placeholderTextColor={colors.text.tertiary}
              value={description}
              onChangeText={setDescription}
              multiline
              numberOfLines={3}
              textAlignVertical="top"
            />
          </View>

          {/* 列 */}
          {columns.length > 0 && (
            <View style={styles.fieldGroup}>
              <Text style={[styles.label, { color: colors.text.secondary }]}>列</Text>
              <View style={styles.chipRow}>
                {columns.map((col) => {
                  const active = col.id === (selectedColumnId ?? columns[0].id);
                  return (
                    <Pressable
                      key={col.id}
                      onPress={() => setSelectedColumnId(col.id)}
                      style={[
                        styles.chip,
                        {
                          backgroundColor: active
                            ? colors.brand.accentLight
                            : colors.bg.muted,
                          borderColor: active
                            ? colors.brand.accent
                            : colors.border.default,
                        },
                      ]}
                    >
                      <Text
                        style={[
                          styles.chipText,
                          {
                            color: active
                              ? colors.brand.accent
                              : colors.text.secondary,
                            fontWeight: active ? '600' : '400',
                          },
                        ]}
                        numberOfLines={1}
                      >
                        {col.name}
                      </Text>
                    </Pressable>
                  );
                })}
              </View>
            </View>
          )}

          {/* 优先级 */}
          <View style={styles.fieldGroup}>
            <Text style={[styles.label, { color: colors.text.secondary }]}>优先级</Text>
            <View style={styles.chipRow}>
              {PRIORITY_OPTIONS.map((opt) => {
                const active = opt.value === priority;
                const priorityColorKey = opt.colorKey;
                const priorityBgKey = `${opt.colorKey}Bg` as 'p1Bg' | 'p2Bg' | 'p3Bg' | 'p4Bg';
                return (
                  <Pressable
                    key={opt.value}
                    onPress={() => setPriority(opt.value)}
                    style={[
                      styles.chip,
                      {
                        backgroundColor: active
                          ? colors.priority[priorityBgKey]
                          : colors.bg.muted,
                        borderColor: active
                          ? colors.priority[priorityColorKey]
                          : colors.border.default,
                      },
                    ]}
                  >
                    <Text
                      style={[
                        styles.chipText,
                        {
                          color: active
                            ? colors.priority[priorityColorKey]
                            : colors.text.secondary,
                          fontWeight: active ? '600' : '400',
                        },
                      ]}
                    >
                      {opt.label}
                    </Text>
                  </Pressable>
                );
              })}
            </View>
          </View>

          {/* Submit button */}
          <TouchableOpacity
            style={[
              styles.submitBtn,
              { backgroundColor: colors.brand.accent },
              (!title.trim() || submitting) && { opacity: 0.5 },
            ]}
            onPress={handleSubmit}
            disabled={!title.trim() || submitting}
            activeOpacity={0.8}
          >
            {submitting ? (
              <ActivityIndicator size="small" color="#fff" />
            ) : (
              <Text style={styles.submitText}>创建 Issue</Text>
            )}
          </TouchableOpacity>
        </BottomSheetView>
      </BottomSheetModal>
    );
  },
);

// ─── Styles ──────────────────────────────────────────────────────────────────

const styles = StyleSheet.create({
  container: {
    flex: 1,
    paddingHorizontal: spacing.xl,
    paddingBottom: spacing.xl,
  },

  header: {
    ...typography.sectionHead,
    marginBottom: spacing.xl,
    marginTop: spacing.sm,
  },

  fieldGroup: {
    marginBottom: spacing.lg,
  },

  label: {
    ...typography.label,
    marginBottom: spacing.sm,
  },

  input: {
    borderWidth: 1,
    borderRadius: radius.md,
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.sm,
    ...typography.body,
  },

  textArea: {
    minHeight: 80,
    paddingTop: spacing.sm,
  },

  chipRow: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    gap: spacing.sm,
  },

  chip: {
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.xs,
    borderRadius: radius.full,
    borderWidth: 1,
    maxWidth: 120,
  },

  chipText: {
    ...typography.label,
  },

  submitBtn: {
    marginTop: spacing.md,
    borderRadius: radius.md,
    paddingVertical: spacing.md,
    alignItems: 'center',
    justifyContent: 'center',
    minHeight: 48,
  },

  submitText: {
    ...typography.bodyMedium,
    color: '#fff',
    fontWeight: '600',
  },
});

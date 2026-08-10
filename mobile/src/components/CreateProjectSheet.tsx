import React, {
  forwardRef,
  useCallback,
  useMemo,
  useState,
} from 'react';
import {
  ActivityIndicator,
  Alert,
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
import { useThemeColors } from '../theme/useTheme';
import { radius, spacing, typography } from '../theme/tokens';

interface CreateProjectSheetProps {
  onCreated: () => void;
}

export const CreateProjectSheet = forwardRef<BottomSheetModal, CreateProjectSheetProps>(
  function CreateProjectSheet({ onCreated }, ref) {
    const colors = useThemeColors();

    const [name, setName] = useState('');
    const [description, setDescription] = useState('');
    const [submitting, setSubmitting] = useState(false);

    const snapPoints = useMemo(() => ['55%'], []);

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

    const handleSheetChange = useCallback((index: number) => {
      if (index === 0) {
        setName('');
        setDescription('');
      }
    }, []);

    const handleSubmit = async () => {
      const trimmedName = name.trim();
      if (!trimmedName) {
        Alert.alert('请填写项目名称', '项目名称为必填项');
        return;
      }
      if (submitting) return;

      setSubmitting(true);
      try {
        // Owner omitted on purpose: when SPA/mobile can't resolve a real
        // caller id, sending owner:{type:'user',id:0} would be rejected by
        // EnsureOwnerWritable. Backend defaults to caller's personal space
        // (server/internal/api/project.go ~244).
        await api.post('/projects', {
          name: trimmedName,
          description: description.trim(),
        });
        (ref as React.RefObject<BottomSheetModal>)?.current?.dismiss();
        onCreated();
      } catch (err) {
        console.warn('CreateProjectSheet submit error:', err);
        Alert.alert('创建失败', '无法创建项目，请稍后重试');
      } finally {
        setSubmitting(false);
      }
    };

    return (
      <BottomSheetModal
        ref={ref}
        index={0}
        snapPoints={snapPoints}
        backdropComponent={renderBackdrop}
        onChange={handleSheetChange}
        enablePanDownToClose
        keyboardBehavior="interactive"
        keyboardBlurBehavior="restore"
        backgroundStyle={{ backgroundColor: colors.bg.surface, borderRadius: 20 }}
        handleIndicatorStyle={{ backgroundColor: colors.border.default, width: 36 }}
      >
        <BottomSheetView style={styles.container}>
          <Text style={[styles.header, { color: colors.text.primary }]}>创建项目</Text>

          <View style={styles.fieldGroup}>
            <Text style={[styles.label, { color: colors.text.secondary }]}>
              项目名称 <Text style={{ color: colors.status.error }}>*</Text>
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
              placeholder="输入项目名称..."
              placeholderTextColor={colors.text.tertiary}
              value={name}
              onChangeText={setName}
              returnKeyType="next"
              maxLength={100}
              autoFocus
            />
          </View>

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
              placeholder="（可选）描述这个项目要做什么..."
              placeholderTextColor={colors.text.tertiary}
              value={description}
              onChangeText={setDescription}
              multiline
              numberOfLines={3}
              textAlignVertical="top"
              maxLength={500}
            />
          </View>

          <TouchableOpacity
            style={[
              styles.submitBtn,
              { backgroundColor: colors.brand.accent },
              (!name.trim() || submitting) && { opacity: 0.5 },
            ]}
            onPress={handleSubmit}
            disabled={!name.trim() || submitting}
            activeOpacity={0.8}
          >
            {submitting ? (
              <ActivityIndicator size="small" color="#fff" />
            ) : (
              <Text style={styles.submitText}>创建项目</Text>
            )}
          </TouchableOpacity>
        </BottomSheetView>
      </BottomSheetModal>
    );
  },
);

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

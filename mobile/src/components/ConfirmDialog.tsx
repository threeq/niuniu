import { View, Text, Modal, TouchableOpacity, StyleSheet } from 'react-native';
import { useThemeColors } from '../theme/useTheme';
import { spacing, typography, radius } from '../theme/tokens';

interface ConfirmDialogProps {
  visible: boolean;
  title: string;
  message: string;
  confirmLabel?: string;
  onConfirm: () => void;
  onCancel: () => void;
  destructive?: boolean;
}

export function ConfirmDialog({
  visible, title, message, confirmLabel = '确认', onConfirm, onCancel, destructive = true,
}: ConfirmDialogProps) {
  const colors = useThemeColors();

  return (
    <Modal visible={visible} transparent animationType="fade" onRequestClose={onCancel}>
      <View style={styles.overlay}>
        <View style={[styles.dialog, { backgroundColor: colors.bg.surface }]}>
          <Text style={[styles.title, { color: colors.text.primary }]}>{title}</Text>
          <Text style={[styles.message, { color: colors.text.secondary }]}>{message}</Text>
          <View style={styles.actions}>
            <TouchableOpacity style={[styles.cancelBtn, { backgroundColor: colors.bg.muted }]} onPress={onCancel}>
              <Text style={[typography.body, { color: colors.text.secondary }]}>取消</Text>
            </TouchableOpacity>
            <TouchableOpacity
              style={[styles.confirmBtn, { backgroundColor: destructive ? colors.status.error : colors.brand.accent }]}
              onPress={() => { onConfirm(); onCancel(); }}
            >
              <Text style={[typography.bodyMedium, { color: '#fff' }]}>{confirmLabel}</Text>
            </TouchableOpacity>
          </View>
        </View>
      </View>
    </Modal>
  );
}

const styles = StyleSheet.create({
  overlay: { flex: 1, backgroundColor: 'rgba(0,0,0,0.5)', justifyContent: 'center', alignItems: 'center' },
  dialog: { borderRadius: 14, padding: spacing.xl, width: '80%', maxWidth: 320 },
  title: { ...typography.sectionHead, marginBottom: spacing.sm },
  message: { ...typography.body, marginBottom: spacing.xl },
  actions: { flexDirection: 'row', gap: spacing.md },
  cancelBtn: { flex: 1, padding: spacing.md, borderRadius: radius.sm, alignItems: 'center' },
  confirmBtn: { flex: 1, padding: spacing.md, borderRadius: radius.sm, alignItems: 'center' },
});

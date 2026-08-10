import React, { forwardRef, useCallback, useImperativeHandle, useRef } from 'react';
import { BottomSheetBackdrop, BottomSheetModal, BottomSheetView } from '@gorhom/bottom-sheet';
import type { BottomSheetBackdropProps } from '@gorhom/bottom-sheet';
import { useThemeColors } from '../theme/useTheme';
import { radius } from '../theme/tokens';

export interface AppBottomSheetRef {
  present: () => void;
  dismiss: () => void;
}

interface AppBottomSheetProps {
  children: React.ReactNode;
  snapPoints?: (string | number)[];
  onClose?: () => void;
}

export const AppBottomSheet = forwardRef<AppBottomSheetRef, AppBottomSheetProps>(
  function AppBottomSheet({ children, snapPoints = ['50%', '90%'], onClose }, ref) {
    const colors = useThemeColors();
    const modalRef = useRef<BottomSheetModal>(null);

    useImperativeHandle(ref, () => ({
      present: () => modalRef.current?.present(),
      dismiss: () => modalRef.current?.dismiss(),
    }));

    const renderBackdrop = useCallback(
      (props: BottomSheetBackdropProps) => (
        <BottomSheetBackdrop {...props} disappearsOnIndex={-1} appearsOnIndex={0} opacity={0.5} />
      ),
      [],
    );

    return (
      <BottomSheetModal
        ref={modalRef}
        snapPoints={snapPoints}
        backdropComponent={renderBackdrop}
        enablePanDownToClose
        enableDynamicSizing={false}
        onDismiss={onClose}
        backgroundStyle={{ backgroundColor: colors.bg.surface, borderRadius: radius.lg }}
        handleIndicatorStyle={{ backgroundColor: colors.border.default, width: 36 }}
      >
        <BottomSheetView>{children}</BottomSheetView>
      </BottomSheetModal>
    );
  },
);

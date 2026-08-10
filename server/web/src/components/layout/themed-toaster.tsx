import { Toaster } from 'sonner';
import { useThemeStore } from '@/stores/theme-store';

export function ThemedToaster() {
  const resolvedTheme = useThemeStore((s) => s.resolvedTheme);
  return (
    <Toaster
      theme={resolvedTheme}
      position="top-right"
      richColors
      closeButton
    />
  );
}

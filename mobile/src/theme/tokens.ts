// Design tokens — Precision Tech Style (Zinc + Indigo)

export const lightColors = {
  bg: {
    base: '#FFFFFF',
    surface: '#F9FAFB',
    muted: '#F3F4F6',
  },
  text: {
    primary: '#111827',
    secondary: '#6B7280',
    tertiary: '#9CA3AF',
  },
  brand: {
    accent: '#4F46E5',
    accentLight: '#EEF2FF',
    team: '#A855F7',
  },
  border: {
    default: '#E5E7EB',
    subtle: '#F3F4F6',
  },
  status: {
    success: '#16A34A',
    successBg: '#F0FDF4',
    warning: '#CA8A04',
    warningBg: '#FEF9C3',
    error: '#DC2626',
    errorBg: '#FEF2F2',
    info: '#4F46E5',
    infoBg: '#EEF2FF',
  },
  priority: {
    p1: '#DC2626',
    p1Bg: '#FEF2F2',
    p2: '#CA8A04',
    p2Bg: '#FEF9C3',
    p3: '#16A34A',
    p3Bg: '#F0FDF4',
    p4: '#6B7280',
    p4Bg: '#F3F4F6',
  },
} as const;

export const darkColors = {
  bg: {
    base: '#09090B',
    surface: '#18181B',
    muted: '#27272A',
  },
  text: {
    primary: '#FAFAFA',
    secondary: '#A1A1AA',
    tertiary: '#71717A',
  },
  brand: {
    accent: '#6366F1',
    accentLight: '#1E1B4B',
    team: '#C084FC',
  },
  border: {
    default: '#27272A',
    subtle: '#18181B',
  },
  status: {
    success: '#4ADE80',
    successBg: '#052E16',
    warning: '#FACC15',
    warningBg: '#422006',
    error: '#F87171',
    errorBg: '#450A0A',
    info: '#818CF8',
    infoBg: '#1E1B4B',
  },
  priority: {
    p1: '#F87171',
    p1Bg: '#450A0A',
    p2: '#FACC15',
    p2Bg: '#422006',
    p3: '#4ADE80',
    p3Bg: '#052E16',
    p4: '#71717A',
    p4Bg: '#27272A',
  },
} as const;

export type ThemeColors = typeof lightColors;

export const typography = {
  display: { fontSize: 28, fontWeight: '700' as const, lineHeight: 32 },
  pageTitle: { fontSize: 22, fontWeight: '700' as const, lineHeight: 26 },
  sectionHead: { fontSize: 17, fontWeight: '600' as const, lineHeight: 22 },
  bodyMedium: { fontSize: 15, fontWeight: '500' as const, lineHeight: 21 },
  body: { fontSize: 15, fontWeight: '400' as const, lineHeight: 21 },
  label: { fontSize: 13, fontWeight: '500' as const, lineHeight: 18 },
  overline: { fontSize: 12, fontWeight: '600' as const, lineHeight: 16, letterSpacing: 0.5, textTransform: 'uppercase' as const },
  caption: { fontSize: 11, fontWeight: '400' as const, lineHeight: 14 },
} as const;

export const spacing = {
  xs: 4,
  sm: 8,
  md: 12,
  lg: 16,
  xl: 24,
  '2xl': 32,
  '3xl': 48,
} as const;

export const radius = {
  xs: 4,
  sm: 8,
  md: 12,
  lg: 18,
  full: 9999,
} as const;

export const shadows = {
  sm: { shadowColor: '#000', shadowOffset: { width: 0, height: 1 }, shadowOpacity: 0.04, shadowRadius: 3, elevation: 1 },
  md: { shadowColor: '#000', shadowOffset: { width: 0, height: 4 }, shadowOpacity: 0.06, shadowRadius: 12, elevation: 3 },
  lg: { shadowColor: '#000', shadowOffset: { width: 0, height: -4 }, shadowOpacity: 0.08, shadowRadius: 20, elevation: 5 },
} as const;

export const PRESET_COLORS = [
  '#d73a4a', '#a2eeef', '#7057ff', '#008672', '#e4e669',
  '#d876e3', '#fbca04', '#0075ca', '#cfd3d7', '#bfd4f2',
] as const;

export function isValidHex(s: string): boolean {
  return /^#[0-9a-f]{6}$/.test(s);
}

export function randomPresetColor(): string {
  return PRESET_COLORS[Math.floor(Math.random() * PRESET_COLORS.length)];
}

export function contrastTextColor(bg: string): string {
  if (!isValidHex(bg)) return '#000';
  const r = parseInt(bg.slice(1, 3), 16);
  const g = parseInt(bg.slice(3, 5), 16);
  const b = parseInt(bg.slice(5, 7), 16);
  const yiq = (r * 299 + g * 587 + b * 114) / 1000;
  return yiq >= 128 ? '#000' : '#fff';
}

const palette = ['#d73a4a', '#7057ff', '#008672', '#0075ca', '#fbca04'];

export function avatarColorFor(name: string): string {
  let h = 0;
  for (const ch of name) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
  return palette[h % palette.length];
}

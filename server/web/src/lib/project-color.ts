// 8-color fixed palette for project decoration in sidebar + kanban.
// Same shape as lib/owner-color.ts. Palette keys are stable strings,
// stored in Project.color server-side.

export interface ProjectColorStyles {
  text: string;
  bgInactive: string;
  bgActive: string;
  borderActive: string;
  swatch: string; // solid bg for the picker swatch button
}

const PALETTE: Record<string, ProjectColorStyles> = {
  emerald: { text: 'text-emerald-500', bgInactive: 'bg-emerald-500/15', bgActive: 'bg-emerald-500/30', borderActive: 'border-l-emerald-500', swatch: 'bg-emerald-500' },
  amber:   { text: 'text-amber-600',   bgInactive: 'bg-amber-500/15',   bgActive: 'bg-amber-500/30',   borderActive: 'border-l-amber-500',   swatch: 'bg-amber-500'   },
  purple:  { text: 'text-purple-500',  bgInactive: 'bg-purple-500/15',  bgActive: 'bg-purple-500/30',  borderActive: 'border-l-purple-500',  swatch: 'bg-purple-500'  },
  rose:    { text: 'text-rose-500',    bgInactive: 'bg-rose-500/15',    bgActive: 'bg-rose-500/30',    borderActive: 'border-l-rose-500',    swatch: 'bg-rose-500'    },
  cyan:    { text: 'text-cyan-600',    bgInactive: 'bg-cyan-500/15',    bgActive: 'bg-cyan-500/30',    borderActive: 'border-l-cyan-500',    swatch: 'bg-cyan-500'    },
  orange:  { text: 'text-orange-500',  bgInactive: 'bg-orange-500/15',  bgActive: 'bg-orange-500/30',  borderActive: 'border-l-orange-500',  swatch: 'bg-orange-500'  },
  indigo:  { text: 'text-indigo-500',  bgInactive: 'bg-indigo-500/15',  bgActive: 'bg-indigo-500/30',  borderActive: 'border-l-indigo-500',  swatch: 'bg-indigo-500'  },
  pink:    { text: 'text-pink-500',    bgInactive: 'bg-pink-500/15',    bgActive: 'bg-pink-500/30',    borderActive: 'border-l-pink-500',    swatch: 'bg-pink-500'    },
};

const NEUTRAL: ProjectColorStyles = {
  text: 'text-muted-foreground',
  bgInactive: 'bg-muted/40',
  bgActive: 'bg-accent',
  borderActive: 'border-l-muted-foreground',
  swatch: 'bg-muted-foreground/40',
};

export const PROJECT_COLOR_KEYS = Object.keys(PALETTE);

export function getProjectColorStyles(color: string | null | undefined): ProjectColorStyles {
  if (!color) return NEUTRAL;
  return PALETTE[color] ?? NEUTRAL;
}

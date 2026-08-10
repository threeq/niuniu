// Deterministic color assignment for owners (orgs) shown in sidebars.
//
// Same org → same color across all three sidebars (project / repository /
// workspace) and stable across refreshes. Personal stays neutral.
//
// Each palette entry exposes four classes used at different points:
//   text         — header text color (chevron + icon + label)
//   bgInactive   — subtle tint on items inside the group (default state)
//   bgActive     — stronger tint when an item is selected
//   borderActive — left-border accent on the selected item
//
// Blue is intentionally absent: the default app accent (bg-info) is blue,
// and we want org tints visually distinct from any unrelated blue chrome.

export interface OwnerStyles {
  text: string;
  bgInactive: string;
  bgActive: string;
  borderActive: string;
}

const ORG_PALETTE: readonly OwnerStyles[] = [
  { text: 'text-emerald-500', bgInactive: 'bg-emerald-500/15', bgActive: 'bg-emerald-500/30', borderActive: 'border-l-emerald-500' },
  { text: 'text-amber-600',   bgInactive: 'bg-amber-500/15',   bgActive: 'bg-amber-500/30',   borderActive: 'border-l-amber-500'   },
  { text: 'text-purple-500',  bgInactive: 'bg-purple-500/15',  bgActive: 'bg-purple-500/30',  borderActive: 'border-l-purple-500'  },
  { text: 'text-rose-500',    bgInactive: 'bg-rose-500/15',    bgActive: 'bg-rose-500/30',    borderActive: 'border-l-rose-500'    },
  { text: 'text-cyan-600',    bgInactive: 'bg-cyan-500/15',    bgActive: 'bg-cyan-500/30',    borderActive: 'border-l-cyan-500'    },
  { text: 'text-orange-500',  bgInactive: 'bg-orange-500/15',  bgActive: 'bg-orange-500/30',  borderActive: 'border-l-orange-500'  },
  { text: 'text-indigo-500',  bgInactive: 'bg-indigo-500/15',  bgActive: 'bg-indigo-500/30',  borderActive: 'border-l-indigo-500'  },
  { text: 'text-pink-500',    bgInactive: 'bg-pink-500/15',    bgActive: 'bg-pink-500/30',    borderActive: 'border-l-pink-500'    },
];

// Personal owners keep the existing app-accent treatment so nothing
// else in the workspace shifts: neutral header, no inactive tint,
// blue active highlight via the existing `info` theme color.
const PERSONAL: OwnerStyles = {
  text: 'text-muted-foreground',
  bgInactive: '',
  bgActive: 'bg-info/10',
  borderActive: 'border-l-info',
};

// Stable string hash (FNV-1a 32-bit).
function hash(s: string): number {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return h >>> 0;
}

// getOwnerStyles returns the full color set for an owner key
// (e.g. "user:42" or "org:5"). Personal owners get the neutral set;
// orgs deterministically map to one ORG_PALETTE entry.
export function getOwnerStyles(ownerKey: string): OwnerStyles {
  if (ownerKey.startsWith('user:')) return PERSONAL;
  return ORG_PALETTE[hash(ownerKey) % ORG_PALETTE.length];
}

// Convenience accessors (kept for the existing OwnerGroupSection call site).
export function getOwnerColor(ownerKey: string): string {
  return getOwnerStyles(ownerKey).text;
}

export function getOwnerBgTint(ownerKey: string): string {
  return getOwnerStyles(ownerKey).bgInactive;
}

// ownerKeyOf builds the canonical "user:<id>" / "org:<id>" key from
// an OwnerRef-shaped object. Returns '' when the input lacks both
// type and id (e.g. legacy items with no owner).
export function ownerKeyOf(owner: { type?: string; id?: number } | undefined): string {
  if (!owner?.type || owner.id == null) return '';
  return `${owner.type}:${owner.id}`;
}

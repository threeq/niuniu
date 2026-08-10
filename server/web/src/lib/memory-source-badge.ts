// Shared source-badge styling for the Memory and Learnings tabs so the same
// source (manual / mcp / extract / ...) renders identically across both — they
// previously hand-rolled divergent maps (learnings used hardcoded purple for
// "extract"; memory used semantic tokens), producing inconsistent badges.
//
// Design-system tokens only (no hex / arbitrary values), dark-mode safe via the
// token variables. Covers the union of both feature's source enums.
export const SOURCE_BADGE_CLASS: Record<string, string> = {
  manual: 'bg-muted text-muted-foreground border-border',
  mcp: 'bg-info/10 text-info border-info/30',
  extract: 'bg-accent text-accent-foreground border-border',
  generate: 'bg-success/10 text-success border-success/30',
  consolidate: 'bg-warning/10 text-warning border-warning/30',
};

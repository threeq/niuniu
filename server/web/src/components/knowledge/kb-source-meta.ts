import {
  FolderOpen,
  Upload,
  Globe,
  Plug,
  type LucideIcon,
} from 'lucide-react'
import type { KBSourceKind } from '@/lib/kb-api'

// Non-component exports for the knowledge-base UI. Kept out of kb-status.tsx so
// that file exports components only (react-refresh/only-export-components).

/** Icon per source kind, shared by every KB surface so the mapping stays stable. */
export const KB_SOURCE_ICON: Record<KBSourceKind, LucideIcon> = {
  local: FolderOpen,
  upload: Upload,
  url: Globe,
  mcp: Plug,
}

/** The four source kinds, in the order the create form presents them. */
export const KB_SOURCE_KINDS: KBSourceKind[] = ['local', 'upload', 'url', 'mcp']

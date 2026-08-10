// Draft persistence for the kanban quick-create dialog. Unlike chat-draft (which
// uses sessionStorage), issue drafts use localStorage so a draft survives a tab
// close / reload (spec 2026-06-06 ss3.2). Keyed by project + column. Any explicit
// finish (save or cancel) clears the draft; only an involuntary close keeps it.
const PREFIX = 'niuniu:issue-draft:'

export type IssuePriority = 'low' | 'medium' | 'high' | 'critical'

export interface IssueDraft {
  title: string
  description: string
  priority: IssuePriority
  parentIssueId: number | null
}

function key(projectId: number, columnId: number): string {
  return `${PREFIX}${projectId}:${columnId}`
}

function isEmpty(d: IssueDraft): boolean {
  return !d.title.trim() && !d.description.trim() && d.parentIssueId == null && d.priority === 'medium'
}

export function saveIssueDraft(projectId: number, columnId: number, draft: IssueDraft): void {
  try {
    if (isEmpty(draft)) {
      localStorage.removeItem(key(projectId, columnId))
      return
    }
    localStorage.setItem(key(projectId, columnId), JSON.stringify(draft))
  } catch {
    // localStorage may be unavailable (private mode / quota) -- ignore.
  }
}

export function loadIssueDraft(projectId: number, columnId: number): IssueDraft | null {
  try {
    const raw = localStorage.getItem(key(projectId, columnId))
    if (!raw) return null
    const parsed = JSON.parse(raw) as Partial<IssueDraft>
    return {
      title: parsed.title ?? '',
      description: parsed.description ?? '',
      priority: (parsed.priority as IssuePriority) ?? 'medium',
      parentIssueId: parsed.parentIssueId ?? null,
    }
  } catch {
    return null
  }
}

export function clearIssueDraft(projectId: number, columnId: number): void {
  try {
    localStorage.removeItem(key(projectId, columnId))
  } catch {
    // ignore
  }
}

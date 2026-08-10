import type { AgentActivity } from '@/types/team'

/**
 * Format an AgentActivity into a one-line display string for the WorkerCard.
 * Returns '' for undefined or unknown kinds (caller should hide the line).
 */
export function formatActivity(a: AgentActivity | undefined): string {
  if (!a) return ''
  switch (a.kind) {
    case 'tool_use':
      return `Running: ${a.tool_name ?? 'tool'}`
    case 'tool_result':
      return `← ${a.tool_name ?? 'tool'} result`
    case 'text':
      return a.text_preview ? `${a.text_preview}…` : ''
    case 'thinking':
      return 'Thinking…'
    case 'inbox_sent':
      return `→ inbox to ${a.target ?? '?'}`
    case 'inbox_received':
      return `← inbox from ${a.target ?? '?'}`
    case 'blackboard_write':
      return `wrote ${a.target ?? ''}`
    case 'dispatched':
      return `Dispatched: ${a.target ?? ''}`
    case 'done':
      return 'Done'
    case 'error':
      return 'Error'
    default:
      return ''
  }
}

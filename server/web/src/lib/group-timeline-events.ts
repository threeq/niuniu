import type { TimelineEvent } from '@/pages/workspaces/panels/chat-message';

/**
 * Groups consecutive tool_use events into arrays for collapsed ToolGroup display.
 * tool_result events are skipped (rendered inline inside tool cards via toolResults map).
 * Any non-tool event (text, thinking, etc.) breaks the current group so that
 * assistant text always remains visible — never hidden inside a collapsed group.
 */
export function groupTimelineEvents(
  events: TimelineEvent[],
): (TimelineEvent | TimelineEvent[])[] {
  const result: (TimelineEvent | TimelineEvent[])[] = [];
  let toolGroup: TimelineEvent[] = [];

  for (let i = 0; i < events.length; i++) {
    const event = events[i];
    if (event.type === 'tool_result') continue;
    if (event.type === 'tool_use') {
      toolGroup.push(event);
    } else {
      if (toolGroup.length > 0) {
        result.push(toolGroup.length === 1 ? toolGroup[0] : [...toolGroup]);
        toolGroup = [];
      }
      result.push(event);
    }
  }
  if (toolGroup.length > 0) {
    result.push(toolGroup.length === 1 ? toolGroup[0] : [...toolGroup]);
  }
  return result;
}

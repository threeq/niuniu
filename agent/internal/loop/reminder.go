package loop

// System reminders: short advisory blocks appended to tool_results at the
// loop level (never inside tools — tools report facts, the loop shapes
// behavior). Format mirrors the [auto-compacted] convention: a bracketed
// tag the model learns to treat as meta-instruction about the conversation,
// not as task content. Byte-stable content only — a reminder that varies
// per round would churn the prompt cache.

// reminderError is appended when a tool execution fails: the model should
// read the error and correct course rather than barrel on.
const reminderError = "[system-reminder] This tool call failed. Read the error above, fix the cause (wrong path, missing dependency, syntax error), and retry with a corrected call. Do not ignore the error or repeat the same call unchanged."

// reminderDenied is appended when the permission layer refuses a tool: the
// model should adjust its approach, not insist.
const reminderDenied = "[system-reminder] This tool call was denied by the permission policy. The user has not approved this kind of action. Choose a different approach that stays within allowed tools, or ask the user to approve it explicitly."

// wrapToolResult merges a tool outcome with its loop-level reminder into
// one text body for the tool_result block.
func wrapToolResult(outcome string, reminder string) string {
	if reminder == "" {
		return outcome
	}
	return outcome + "\n\n" + reminder
}

// reminderFor classifies a tool outcome into the matching reminder. Only
// failures get reminders — a quiet success stays byte-stable.
func reminderFor(toolErr error, denied bool) string {
	switch {
	case denied:
		return reminderDenied
	case toolErr != nil:
		return reminderError
	default:
		return ""
	}
}

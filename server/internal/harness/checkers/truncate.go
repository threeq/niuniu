package checkers

// truncate returns s truncated to at most max bytes, appending "..." if truncated.
// Shared by the command- and judge-class checkers to cap captured output before it
// is stored on a CheckResult.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

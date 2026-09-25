// Package perm classifies tools by risk and decides whether the agent may
// run them. Read-only tools are always allowed; mutating tools (Write, Edit,
// Bash) depend on policy: auto-approved with -y, denied in bare headless
// mode, or escalated to the client via ACP request_permission.
package perm

// Decision is the outcome of a permission check.
type Decision int

const (
	// Allow runs the tool.
	Allow Decision = iota
	// Deny refuses it (reported to the model as an error tool_result).
	Deny
	// Ask escalates to the host (ACP request_permission); in headless mode
	// ask degrades to Deny.
	Ask
)

// Checker decides whether a tool may run.
type Checker interface {
	Check(toolName string) Decision
}

// writeTools are the mutating tools — everything else is read-only/safe.
var writeTools = map[string]bool{
	"Write": true,
	"Edit":  true,
	"Bash":  true,
}

// IsWrite reports whether the named tool can mutate the system.
func IsWrite(toolName string) bool { return writeTools[toolName] }

// Policy is a static rule set. Headless mode uses it; ACP uses an
// interactive checker instead.
type Policy struct {
	// ApproveAll mirrors the -y flag: mutations run without asking.
	ApproveAll bool
}

// NewPolicy returns the headless policy for the given approve-all flag.
func NewPolicy(approveAll bool) *Policy { return &Policy{ApproveAll: approveAll} }

// Check implements Checker. Without ApproveAll, headless mode has nobody to
// ask, so Ask degrades to Deny with a clear message to the model.
func (p *Policy) Check(toolName string) Decision {
	if !IsWrite(toolName) {
		return Allow
	}
	if p.ApproveAll {
		return Allow
	}
	return Deny
}

// DenyMessage is the tool_result text when a headless mutation is refused.
const DenyMessage = "ERROR: permission denied — tool modifies the system and mutations require " +
	"approval (run with -y to auto-approve, or use the interactive/ACP mode)"

// AllowAll is the checker that approves everything (library default).
type allowAll struct{}

func (allowAll) Check(string) Decision { return Allow }

// AllowAllChecker approves every tool.
func AllowAllChecker() Checker { return allowAll{} }

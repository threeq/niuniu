package agentproxy

import (
	"strings"

	"github.com/shirou/gopsutil/v4/process"
)

// shellNames lists process names recognized as shell processes (the kind
// Claude CLI's Bash[bg] spawns). On Windows gopsutil preserves the .exe
// suffix; both forms are matched after ToLower normalization. We deliberately
// exclude node/npx/python/etc — those are MCP server children, not shells,
// even though bash bg can launch them. We only need to count the SHELL: once
// it exits, the tracker entry is fair game per Claude CLI's bg model.
var shellNames = map[string]struct{}{
	"bash":           {},
	"bash.exe":       {},
	"sh":             {},
	"sh.exe":         {},
	"zsh":            {},
	"zsh.exe":        {},
	"fish":           {},
	"fish.exe":       {},
	"dash":           {},
	"cmd.exe":        {},
	"powershell.exe": {},
	"pwsh":           {},
	"pwsh.exe":       {},
}

// countAliveShellDescendantsFn is the live probe — overridable from tests via
// package-level variable swap to avoid hitting real gopsutil in unit tests.
// Default impl walks the process tree rooted at cliPid via gopsutil.
var countAliveShellDescendantsFn = countAliveShellDescendantsImpl

// countAliveShellDescendantsImpl returns the number of descendant processes
// of cliPid whose Name() matches a shell binary. Returns -1 on probe error
// (caller should skip the GC decision rather than risk wrongly clearing live
// shells). The CLI process itself is excluded because Children() does not
// include the parent. Other long-lived MCP children (niuniu-mcp, npx for
// community MCP servers) are ignored — they aren't shells.
//
// LIMITATIONS:
//   - A bash bg that daemonizes a child and exits (e.g. `bash -c "python app.py &"`)
//     leaves no shell descendant: we count 0 → the tracker entry gets cleared
//     even though the user's detached process is still going. Acceptable: the
//     tracker is named after the SHELL, not its detached children.
//   - On Windows gopsutil queries WMI for Name()/Children(); slow on overloaded
//     hosts. Bounded by the 10s GC tick + per-session early-exit when bash count
//     is zero.
func countAliveShellDescendantsImpl(cliPid int32) int {
	if cliPid <= 0 {
		return -1
	}
	p, err := process.NewProcess(cliPid)
	if err != nil {
		return -1
	}
	// Probe error on the root counts as -1 so callers skip. Recursion past
	// the root absorbs per-node errors as 0 (mid-walk parent-exit is normal).
	return walkShells(p, 0, 16)
}

func walkShells(p *process.Process, depth, maxDepth int) int {
	if depth > maxDepth {
		return 0
	}
	children, err := p.Children()
	if err != nil {
		return 0
	}
	total := 0
	for _, c := range children {
		if name, err := c.Name(); err == nil {
			if _, ok := shellNames[strings.ToLower(name)]; ok {
				total++
			}
		}
		total += walkShells(c, depth+1, maxDepth)
	}
	return total
}

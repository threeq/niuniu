package localrunner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// audit.go implements the local, append-only audit trail (#473): every command
// and path decision the gateway makes is recorded to disk so放行/拒绝 are always
// reviewable after the fact. The trail is local-only — it is never sent to the
// remote, matching "判定只在本地".

// AuditEntry is one gateway decision.
type AuditEntry struct {
	TS         int64  `json:"ts"` // unix milliseconds
	Kind       string `json:"kind"`
	Command    string `json:"command,omitempty"`
	Path       string `json:"path,omitempty"`
	WorkingDir string `json:"working_dir,omitempty"`
	Allowed    bool   `json:"allowed"`
	Reason     string `json:"reason"`
}

// Auditor records gateway decisions.
type Auditor interface {
	Record(AuditEntry)
}

// FileAuditor appends decisions as JSON lines to a file. Safe for concurrent
// use. A write failure is swallowed (best-effort): the audit trail must never
// block or fail an execution decision, and the primary safety guarantee is the
// deny itself, not the log line.
type FileAuditor struct {
	mu   sync.Mutex
	path string
}

// NewFileAuditor creates an auditor writing to path (parent dirs created lazily
// on first write).
func NewFileAuditor(path string) *FileAuditor {
	return &FileAuditor{path: path}
}

// Record appends one JSON line. If the entry has no timestamp, wall-clock is
// stamped now.
func (a *FileAuditor) Record(e AuditEntry) {
	if e.TS == 0 {
		e.TS = time.Now().UnixMilli()
	}
	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(a.path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(a.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

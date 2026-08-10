package localrunner

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	securejoin "github.com/cyphar/filepath-securejoin"
)

// gateway.go is the security bottom line (#473). Every command and every file
// path from the remote side passes through here BEFORE anything runs or is
// read. The rules, restated from the spec and non-negotiable:
//
//   - Whitelist is DEFAULT-DENY (fail-safe): anything not positively matched is
//     treated as unauthorized and must be confirmed by the local user.
//   - Anything with shell control operators can never be auto-allowed by a bare
//     program name — a chained command (`git status && rm -rf /`) always needs
//     explicit approval, so a whitelisted head can't smuggle an unlisted tail.
//   - The bound directory is a HARD boundary: absolute paths and `..` escapes
//     are rejected, and SecureJoin clamps everything else inside the root.
//   - Every allow/deny is written to a local audit log.
//   - We NEVER trust the remote's claim about whether a command is "safe" — the
//     judgement happens only here, locally.

// ApprovalRequest is shown to the user for an unlisted command. It always
// carries the FULL command and the working directory (spec requirement).
type ApprovalRequest struct {
	Command    string
	WorkingDir string
}

// ApprovalResult is the user's decision from the native prompt.
type ApprovalResult struct {
	// Allow lets this one invocation proceed. false = rejected.
	Allow bool
	// Always, when Allow is true, means "始终允许" — persist so future
	// invocations skip the prompt (only honored if AlwaysAllowPersist is on).
	Always bool
}

// Approver renders the native confirmation prompt. The app layer implements it
// with a Wails/OS-native dialog; tests supply a fake. A nil Approver means
// there is no one to ask, so unlisted commands are denied outright (fail-safe).
type Approver interface {
	Approve(req ApprovalRequest) ApprovalResult
}

// PersistFunc is called to add a newly "always allow"ed entry to the durable
// whitelist (the app wires it to the server config / registry). Best-effort:
// an error is audited but the in-memory allow still stands for this session.
type PersistFunc func(entry string) error

// GatewayConfig assembles a Gateway.
type GatewayConfig struct {
	// Dir is the bound local directory — the hard boundary root. Required.
	Dir string
	// Allowed is the initial command whitelist (program names like "npm", or
	// exact full-command strings).
	Allowed []string
	// AlwaysAllowPersist enables honoring "始终允许" (persisting approvals).
	AlwaysAllowPersist bool
	// Approver prompts the user for unlisted commands (nil ⇒ deny unlisted).
	Approver Approver
	// Persist durably records "始终允许" entries (nil ⇒ session-only).
	Persist PersistFunc
	// Audit records every decision (nil ⇒ decisions are not logged; the app
	// should always supply one so the audit trail requirement holds).
	Audit Auditor
}

// Gateway enforces the local security policy for one bound runner.
type Gateway struct {
	dir                string
	alwaysAllowPersist bool
	approver           Approver
	persist            PersistFunc
	audit              Auditor

	mu      sync.Mutex
	allowed map[string]bool
}

// NewGateway builds a Gateway from cfg. Dir is cleaned; the whitelist is copied.
func NewGateway(cfg GatewayConfig) *Gateway {
	allowed := make(map[string]bool, len(cfg.Allowed))
	for _, c := range cfg.Allowed {
		if c = strings.TrimSpace(c); c != "" {
			allowed[c] = true
		}
	}
	return &Gateway{
		dir:                filepath.Clean(cfg.Dir),
		alwaysAllowPersist: cfg.AlwaysAllowPersist,
		approver:           cfg.Approver,
		persist:            cfg.Persist,
		audit:              cfg.Audit,
		allowed:            allowed,
	}
}

// Dir returns the bound directory (the boundary root).
func (g *Gateway) Dir() string { return g.dir }

// AuthorizeCommand decides whether command may run. Returns (true, reason) to
// proceed or (false, reason) to reject; reason is human-readable for the audit
// log and the response error. It may block on the native prompt for unlisted
// commands. now is injected so audit timestamps are testable.
func (g *Gateway) AuthorizeCommand(command string, now func() int64) (bool, string) {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		g.record(now, AuditEntry{Kind: "command", Command: command, WorkingDir: g.dir, Allowed: false, Reason: "empty command"})
		return false, "empty command"
	}

	// Exact-string whitelist hit (covers "always allow"ed complex commands).
	g.mu.Lock()
	if g.allowed[trimmed] {
		g.mu.Unlock()
		g.record(now, AuditEntry{Kind: "command", Command: command, WorkingDir: g.dir, Allowed: true, Reason: "whitelist: exact"})
		return true, "whitelist: exact"
	}
	// Program-name whitelist hit, but ONLY for a simple single command with no
	// shell operators — otherwise a listed head could chain an unlisted tail.
	simple := !hasShellControl(trimmed)
	prog := firstToken(trimmed)
	listed := simple && prog != "" && g.allowed[prog]
	g.mu.Unlock()
	if listed {
		g.record(now, AuditEntry{Kind: "command", Command: command, WorkingDir: g.dir, Allowed: true, Reason: "whitelist: " + prog})
		return true, "whitelist: " + prog
	}

	// Not whitelisted → ask the user. No approver ⇒ fail-safe deny.
	if g.approver == nil {
		g.record(now, AuditEntry{Kind: "command", Command: command, WorkingDir: g.dir, Allowed: false, Reason: "not whitelisted, no approver"})
		return false, "command not in whitelist and no approver available"
	}
	res := g.approver.Approve(ApprovalRequest{Command: trimmed, WorkingDir: g.dir})
	if !res.Allow {
		g.record(now, AuditEntry{Kind: "command", Command: command, WorkingDir: g.dir, Allowed: false, Reason: "user denied"})
		return false, "command denied by user"
	}

	reason := "user approved (once)"
	if res.Always && g.alwaysAllowPersist {
		// Persist a program name for a simple command, else the exact string.
		entry := trimmed
		if simple && prog != "" {
			entry = prog
		}
		g.mu.Lock()
		g.allowed[entry] = true
		g.mu.Unlock()
		reason = "user approved (always: " + entry + ")"
		if g.persist != nil {
			if err := g.persist(entry); err != nil {
				g.record(now, AuditEntry{Kind: "command", Command: command, WorkingDir: g.dir, Allowed: true, Reason: "persist failed: " + err.Error()})
			}
		}
	}
	g.record(now, AuditEntry{Kind: "command", Command: command, WorkingDir: g.dir, Allowed: true, Reason: reason})
	return true, reason
}

// ResolvePath maps a remote-supplied relative path to an absolute path inside
// the bound directory, enforcing the hard boundary. Absolute inputs and `..`
// escapes are rejected outright; everything else is SecureJoin'd (which itself
// can never escape the root). Returns (absPath, nil) on success.
func (g *Gateway) ResolvePath(rel string, now func() int64) (string, error) {
	cleanedRel := strings.TrimSpace(rel)
	if cleanedRel == "" {
		cleanedRel = "."
	}
	if filepath.IsAbs(cleanedRel) || hasDotDotEscape(cleanedRel) {
		g.record(now, AuditEntry{Kind: "path", Path: rel, WorkingDir: g.dir, Allowed: false, Reason: "path escapes bound directory"})
		return "", fmt.Errorf("path %q escapes the bound directory", rel)
	}
	abs, err := securejoin.SecureJoin(g.dir, cleanedRel)
	if err != nil {
		g.record(now, AuditEntry{Kind: "path", Path: rel, WorkingDir: g.dir, Allowed: false, Reason: "securejoin: " + err.Error()})
		return "", fmt.Errorf("resolve path %q: %w", rel, err)
	}
	g.record(now, AuditEntry{Kind: "path", Path: rel, WorkingDir: g.dir, Allowed: true, Reason: "within bound directory"})
	return abs, nil
}

func (g *Gateway) record(now func() int64, e AuditEntry) {
	if g.audit == nil {
		return
	}
	if now != nil {
		e.TS = now()
	}
	g.audit.Record(e)
}

// hasShellControl reports whether s contains any shell metacharacter that could
// chain, redirect, expand, or subshell — i.e. anything that means "the first
// token is not the whole story". Aggressively fail-safe by design.
func hasShellControl(s string) bool {
	return strings.ContainsAny(s, ";&|`$><(){}\n\r")
}

// firstToken returns the leading whitespace-delimited token of s.
func firstToken(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// hasDotDotEscape reports whether a slash/backslash path contains a ".."
// component (which, absolute or not, signals an escape attempt).
func hasDotDotEscape(p string) bool {
	norm := strings.ReplaceAll(p, "\\", "/")
	return slices.Contains(strings.Split(norm, "/"), "..")
}

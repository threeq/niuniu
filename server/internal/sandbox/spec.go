package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// Runtime identifiers for the in-box language engine. The runtime is
// orthogonal to isolation: all of them execute inside the same OS fence.
//
// Note on the wazero tier's CPU story, because #739 asked for it to be checked
// at implementation time rather than assumed: wazero has NO deterministic fuel
// or instruction-counting meter. Its published API offers a hard linear-memory
// cap and context-based cancellation, and nothing that charges per instruction
// (checked against wazero's own documentation — there is no fuel API to
// configure). So the wazero tier bounds CPU exactly the way the goja tier does:
// the wall clock plus the in-box interrupt, backed by the OS fence. It is NOT a
// CPU-metering upgrade over goja; its advantage is the memory ceiling, which
// goja does not have.
//
// The Node/Deno tier is declared for the heavy-npm case the spec describes; it
// is a different in-box process image, not a different isolation model.
const (
	RuntimeGoja   = "goja"   // pure-Go JS engine — the default (zero toolchain,即写即跑)
	RuntimeWazero = "wazero" // pure-Go WASM runtime — declared, not yet implemented
	RuntimeNode   = "node"   // Node/Deno tier for heavy npm dependencies — declared, not yet implemented
)

// implementedRuntimes is the set this build can actually execute. Declaring a
// runtime the build cannot run is rejected at draft time rather than failing at
// the first call.
var implementedRuntimes = map[string]bool{RuntimeGoja: true}

// RuntimeImplemented reports whether this build can execute the runtime.
func RuntimeImplemented(name string) bool { return implementedRuntimes[normalizeRuntime(name)] }

func normalizeRuntime(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return RuntimeGoja
	}
	return n
}

// EgressOp is one operation the function is allowed to reach. Authorization is
// by OPERATION, never by URL: the function cannot express a host, a scheme or a
// path, so a function can never widen its own reach.
type EgressOp struct {
	// Operation is a contract operation name (preferred form).
	Operation string `json:"operation,omitempty"`
	// Method+Path is the legacy form for providers whose contract has no named
	// operations. It is still matched against the provider whitelist at egress.
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`
	// ReadOnly mirrors the contract's read-only flag; it is what the publish
	// card shows and what the write gate is checked against.
	ReadOnly bool `json:"read_only,omitempty"`
	// Sample is the typical input used by the behaviour rehearsal.
	Sample map[string]any `json:"sample,omitempty"`
}

// Key is a stable identifier for a declared egress operation, used to diff two
// versions (a rollback that widens egress must be re-confirmed) and to render
// the confirmation card.
func (e EgressOp) Key() string {
	if op := strings.TrimSpace(e.Operation); op != "" {
		return "operation:" + op
	}
	return strings.ToUpper(strings.TrimSpace(e.Method)) + " " + strings.TrimSpace(e.Path)
}

// Spec is the immutable, hashable description of one published operation
// function. Everything the host needs to run it lives here; nothing is read
// from the live tables at call time except the provider contract.
type Spec struct {
	Name        string
	Description string
	Runtime     string
	Code        string
	Entry       string
	Limits      Limits
	Egress      []EgressOp
	// Idempotent declares whether replaying the call is safe. A non-idempotent
	// function is never auto-replayed by the platform (no double charging), and
	// a repeated idempotency key is refused rather than re-run.
	Idempotent bool
}

// Normalize fills defaults and validates the spec, returning a copy safe to
// execute. It is called on the draft path (so a bad spec fails at review time)
// and again before every run (so nothing skips validation).
func (s Spec) Normalize() (Spec, error) {
	out := s
	out.Name = strings.TrimSpace(out.Name)
	if out.Name == "" {
		return out, fmt.Errorf("function name is required")
	}
	if !nameRe.MatchString(out.Name) {
		return out, fmt.Errorf("function name %q must match %s", out.Name, nameRe.String())
	}
	out.Runtime = normalizeRuntime(out.Runtime)
	if !RuntimeImplemented(out.Runtime) {
		return out, fmt.Errorf("runtime %q is not available in this build", out.Runtime)
	}
	if strings.TrimSpace(out.Code) == "" {
		return out, fmt.Errorf("function code is required")
	}
	if len(out.Code) > MaxCodeBytes {
		return out, fmt.Errorf("function code is %d bytes; the limit is %d", len(out.Code), MaxCodeBytes)
	}
	out.Entry = strings.TrimSpace(out.Entry)
	if out.Entry == "" {
		out.Entry = "main"
	}
	out.Limits = out.Limits.normalized()
	if len(out.Egress) == 0 {
		return out, fmt.Errorf("function declares no egress operation; a function with no reachable operation cannot do anything useful")
	}
	seen := make(map[string]bool, len(out.Egress))
	for i, e := range out.Egress {
		key := e.Key()
		if key == "operation:" || key == " " {
			return out, fmt.Errorf("egress entry %d names neither an operation nor a method+path", i)
		}
		if seen[key] {
			return out, fmt.Errorf("egress entry %d (%s) is declared twice", i, key)
		}
		seen[key] = true
	}
	return out, nil
}

// CodeHash pins the exact source. Every call records the hash it ran and every
// version row carries it, so "which body was live when this happened" stays
// answerable and a rollback restores a byte-identical program rather than
// "whatever the row says now".
func (s Spec) CodeHash() string {
	sum := sha256.Sum256([]byte(s.Code))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// nameRe constrains function names to something usable both as an identifier
// and inside a projected MCP tool name (ext__<source>__<operation>).
var nameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,63}$`)

// Allows reports whether an egress request is covered by the spec. A request
// carrying an operation is matched on the operation name; otherwise the legacy
// method+path pair is matched.
func (s Spec) Allows(req *HTTPRequestMessage) bool {
	if op := strings.TrimSpace(req.Operation); op != "" {
		for _, e := range s.Egress {
			if strings.TrimSpace(e.Operation) == op {
				return true
			}
		}
		return false
	}
	// A request with no operation may not be resolved by the host either; the
	// caller must name an operation. Reject so the failure is explicit.
	return false
}

// ToolName is the MCP tool name a published function projects to.
func ToolName(source, function string) string {
	return "ext__" + sanitizeToolSegment(source) + "__" + sanitizeToolSegment(function)
}

func sanitizeToolSegment(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		out = "x"
	}
	return out
}

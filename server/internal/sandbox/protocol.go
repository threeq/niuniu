// Package sandbox runs AI-authored "operation functions": small programs that
// turn one integration operation into the HTTP calls it needs, executed in a
// throwaway child process that is fenced by the operating system.
//
// Two independent layers (issue #739):
//
//   - Capability layer — inside the box the function has no ambient authority.
//     It sees its input, a captured log, and exactly one host function,
//     http_request(...). No filesystem, no sockets, no environment.
//   - Fence layer — the box always runs in its own process, further confined by
//     an OS-native sandbox (Windows AppContainer + job object, macOS seatbelt,
//     Linux user/net namespaces + Landlock + seccomp). When the fence for this
//     platform is unavailable the host REFUSES to run a function: degradation is
//     reported, never silently accepted.
//
// http_request never touches the network itself. The box emits a frame on
// stdout, the host services it through the ordinary external-proxy egress path
// (operation authorization + credential injection + SSRF guard + audit) and
// writes the response back on stdin. Swapping the in-box runtime (goja /
// wazero / node) therefore never touches the isolation model.
package sandbox

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// Kind is the frame discriminator on the stdio wire.
type Kind string

const (
	KindInvoke       Kind = "invoke"        // host -> box
	KindHTTPRequest  Kind = "http_request"  // box  -> host
	KindHTTPResponse Kind = "http_response" // host -> box
	KindLog          Kind = "log"           // box  -> host
	KindResult       Kind = "result"        // box  -> host
)

// maxFrameBytes bounds a single frame in either direction. A box that tries to
// emit more is killed by the reader rather than being allowed to exhaust host
// memory, so this doubles as the "output size" limit's last line of defence.
const maxFrameBytes = 8 << 20

// Error codes carried by BoxError. They are stable identifiers the UI, the
// audit table and the agent all key off, so never reuse one for a new meaning.
const (
	ErrLintFailed       = "lint_failed"
	ErrFenceUnavailable = "fence_unavailable"
	ErrSpawnFailed      = "spawn_failed"
	ErrProtocol         = "protocol_error"
	ErrTimeout          = "timeout"
	ErrMemoryLimit      = "memory_limit"
	ErrOutputLimit      = "output_limit"
	ErrRuntime          = "runtime_error"
	ErrEgressDenied     = "egress_denied"
	ErrEgressLimit      = "egress_limit"
	ErrKilled           = "killed"
)

// InvokeMessage starts a run. It is the only host -> box frame that carries
// code, and it is written after the fence is fully applied.
type InvokeMessage struct {
	Kind    Kind           `json:"kind"`
	Name    string         `json:"name"`
	Runtime string         `json:"runtime"`
	Code    string         `json:"code"`
	Entry   string         `json:"entry"`
	Input   map[string]any `json:"input"`
	Limits  Limits         `json:"limits"`
}

// HTTPRequestMessage asks the host to perform one outbound call.
//
// It deliberately cannot express a URL: the function names a contract
// OPERATION (plus its parameters) and the host resolves method + path from the
// provider contract. Dynamic URL construction is therefore not expressible —
// not merely linted against.
type HTTPRequestMessage struct {
	Kind      Kind           `json:"kind"`
	ID        int64          `json:"id"`
	Operation string         `json:"operation"`
	Params    map[string]any `json:"params,omitempty"`
	Query     map[string]any `json:"query,omitempty"`
	Body      map[string]any `json:"body,omitempty"`
}

// HTTPResponseMessage is the host's answer to one HTTPRequestMessage. A failed
// call is reported in-band (OK=false) so the function can branch on it instead
// of dying, which is what makes multi-step flows (exchange a token, then use
// it) writable.
type HTTPResponseMessage struct {
	Kind    Kind              `json:"kind"`
	ID      int64             `json:"id"`
	OK      bool              `json:"ok"`
	Status  int               `json:"status,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
	Error   string            `json:"error,omitempty"`
}

// LogMessage streams one line of captured console output.
type LogMessage struct {
	Kind Kind   `json:"kind"`
	Text string `json:"text"`
}

// ResultMessage terminates a run.
type ResultMessage struct {
	Kind        Kind      `json:"kind"`
	OK          bool      `json:"ok"`
	Output      any       `json:"output,omitempty"`
	Error       *BoxError `json:"error,omitempty"`
	Logs        []string  `json:"logs,omitempty"`
	EgressCalls int       `json:"egress_calls,omitempty"`
}

// BoxError is a structured failure raised inside the box.
type BoxError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *BoxError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// WriteFrame writes one length-prefixed JSON frame.
func WriteFrame(w io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("sandbox: encode frame: %w", err)
	}
	if len(body) > maxFrameBytes {
		return fmt.Errorf("sandbox: frame of %d bytes exceeds the %d byte limit", len(body), maxFrameBytes)
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(body)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	return nil
}

// ReadFrame reads one length-prefixed JSON frame and returns its kind together
// with the raw body, so a caller can decode the concrete message type.
func ReadFrame(r *bufio.Reader) (Kind, []byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return "", nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 {
		return "", nil, fmt.Errorf("sandbox: empty frame")
	}
	if n > maxFrameBytes {
		return "", nil, fmt.Errorf("sandbox: frame of %d bytes exceeds the %d byte limit", n, maxFrameBytes)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return "", nil, err
	}
	var head struct {
		Kind Kind `json:"kind"`
	}
	if err := json.Unmarshal(body, &head); err != nil {
		return "", nil, fmt.Errorf("sandbox: decode frame header: %w", err)
	}
	return head.Kind, body, nil
}

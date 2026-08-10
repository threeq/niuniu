// Package localrunner implements the desktop-side local execution engine for
// Epic #526·子D: the reverse-channel client that connects to a bound remote
// niuniu node, receives command/read/sync requests over a WebSocket, enforces
// the local security gateway, executes inside the bound directory, and streams
// stdout/stderr/exit back to the server.
//
// The wire protocol is dictated by the server (server/internal/service and
// server/internal/api local_runner.go) and must be matched exactly — this
// package only speaks it, it never changes its semantics.
//
// Server → client frames (received on the reverse channel):
//
//	{"type":"request","id":"req-N","kind":"exec|read|sync","command":"...","path":"..."}
//
// Client → server frames (sent back):
//
//	{"type":"log","level":"stdout|stderr|system|command","text":"..."}  // live streaming
//	{"type":"exit","code":N}                                            // command finished
//	{"type":"response","id":"req-N","ok":bool,"stdout":"...","stderr":"...","exit":N,"content":"...","error":"..."}
//	{"type":"pong"}                                                     // heartbeat keep-alive
//
// A response frame resolves the server's pending request() call (routed by id);
// log/exit frames feed the server's log ring buffer / SPA log stream.
package localrunner

// requestFrame is a server→client command dispatch. Only the fields relevant to
// the kind are populated: command for exec, path for read, neither for sync.
type requestFrame struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Command string `json:"command"`
	Path    string `json:"path"`
}

// logFrame streams one live output line to the server log hub.
type logFrame struct {
	Type  string `json:"type"`  // always "log"
	Level string `json:"level"` // stdout | stderr | system | command
	Text  string `json:"text"`
}

// exitFrame marks the end of an exec command.
type exitFrame struct {
	Type string `json:"type"` // always "exit"
	Code int    `json:"code"`
}

// responseFrame answers a request, routed back by id.
type responseFrame struct {
	Type    string `json:"type"` // always "response"
	ID      string `json:"id"`
	OK      bool   `json:"ok"`
	Stdout  string `json:"stdout,omitempty"`
	Stderr  string `json:"stderr,omitempty"`
	Exit    int    `json:"exit"`
	Content string `json:"content,omitempty"`
	Error   string `json:"error,omitempty"`
}

// pongFrame is the app-level heartbeat that keeps the server's read deadline
// (localRunnerPongWait, 35s) from firing while no command is in flight.
type pongFrame struct {
	Type string `json:"type"` // always "pong"
}

// Frame kinds and levels, named so the client and tests share one spelling.
const (
	kindExec = "exec"
	kindRead = "read"
	kindSync = "sync"

	levelStdout  = "stdout"
	levelStderr  = "stderr"
	levelSystem  = "system"
	levelCommand = "command"
)

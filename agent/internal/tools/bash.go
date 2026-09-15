package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
)

// Bash executes a shell command and returns its combined output. On Windows
// it runs through `cmd /c`; elsewhere `sh -c`. Output is capped so a chatty
// command cannot flood the context; non-zero exits are reported inside the
// result (the model needs the output to debug, so they are not tool errors).
type Bash struct{}

// bashDefaults bound one invocation.
const (
	bashDefaultTimeout = 120 * time.Second
	bashMaxTimeout     = 10 * time.Minute
	bashMaxOutput      = 30 << 10 // 30 KiB
)

// bashInput.
type bashInput struct {
	Command   string `json:"command"`
	TimeoutMS int    `json:"timeout_ms"`
}

func (Bash) Def() model.ToolDef {
	return model.ToolDef{
		Name: "Bash",
		Description: "Executes a shell command and returns its combined stdout+stderr " +
			"(output capped at 30 KiB). On Windows commands run via cmd /c; elsewhere via sh -c. " +
			"Set timeout_ms (default 120000, max 600000) for long-running commands.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"command":{"type":"string","description":"The shell command to execute"},` +
			`"timeout_ms":{"type":"integer","description":"Timeout in milliseconds (default 120000, max 600000)"}},` +
			`"required":["command"]}`),
	}
}

func (Bash) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in bashInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(in.Command) == "" {
		return "", fmt.Errorf("command is required")
	}
	timeout := bashDefaultTimeout
	if in.TimeoutMS > 0 {
		timeout = time.Duration(in.TimeoutMS) * time.Millisecond
		if timeout > bashMaxTimeout {
			timeout = bashMaxTimeout
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/c", in.Command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", in.Command)
	}

	// Combined output; exec copies stdout and stderr from separate pipes in
	// separate goroutines, so the buffer must be mutex-guarded.
	var out syncBuffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	runErr := cmd.Run()

	text := strings.TrimRight(out.String(), "\n")
	if text == "" {
		text = "(no output)"
	}
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("command timed out after %s\n%s", timeout, text), nil
	}
	if runErr != nil {
		return fmt.Sprintf("command failed: %v\n%s", runErr, text), nil
	}
	return text, nil
}

// syncBuffer is a mutex-guarded byte buffer.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.b.Len()+len(p) > bashMaxOutput*4 { // hard ceiling; trimmed again below
		return 0, nil // drop excess silently
	}
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.b.Bytes()
	if len(b) > bashMaxOutput {
		return fmt.Sprintf("…(output truncated at %d bytes)…\n%s", bashMaxOutput, b[len(b)-bashMaxOutput:])
	}
	return string(b)
}

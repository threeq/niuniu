package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"sync"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
)

// Background Bash: long-running commands (dev servers, big builds, test
// suites) are started with Bash{"run_in_background":true}, which returns a
// bash_id immediately; the BashOutput tool polls accumulated output and
// exit status. Processes are killed when the agent process exits (their
// lifetime is the session's).

type bgProcess struct {
	cmd    *exec.Cmd
	buf    *syncBuffer
	done   chan struct{}
	code   int
	exited bool
}

var (
	bgMu    sync.Mutex
	bgProcs = map[string]*bgProcess{}
	bgSeq   int
)

// startBackgroundBash spawns cmd detached from the caller's timeout: its
// context is the agent's, not the Bash call's.
func startBackgroundBash(command string) (string, *bgProcess) {
	bgMu.Lock()
	defer bgMu.Unlock()
	bgSeq++
	id := fmt.Sprintf("bash-%d", bgSeq)

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", command)
	} else {
		cmd = exec.Command("sh", "-c", command)
	}
	p := &bgProcess{cmd: cmd, buf: &syncBuffer{}, done: make(chan struct{})}
	cmd.Stdout = p.buf
	cmd.Stderr = p.buf
	if err := cmd.Start(); err != nil {
		p.buf.Write([]byte("failed to start: " + err.Error()))
		p.code = -1
		p.exited = true
		close(p.done)
		return id, p
	}
	go func() {
		err := cmd.Wait()
		bgMu.Lock()
		p.code = exitCode(err)
		p.exited = true
		bgMu.Unlock()
		close(p.done)
	}()
	bgProcs[id] = p
	return id, p
}

func shellName() string {
	if runtime.GOOS == "windows" {
		return "cmd"
	}
	return "sh"
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	type coder interface{ ExitCode() int }
	if c, ok := err.(coder); ok {
		return c.ExitCode()
	}
	return -1
}

// BashOutput polls one background bash: full accumulated output (capped)
// plus run status.
type BashOutput struct{}

func (BashOutput) Def() model.ToolDef {
	return model.ToolDef{
		Name:        "BashOutput",
		Description: "Fetch the accumulated output and run status of a background bash started with Bash(run_in_background=true). Call repeatedly to follow progress; the process keeps running between calls.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"bash_id":{"type":"string","description":"Id returned by Bash"}},` +
			`"required":["bash_id"]}`),
	}
}

func (BashOutput) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		BashID string `json:"bash_id"`
	}
	if err := json.Unmarshal(input, &in); err != nil || in.BashID == "" {
		return "", fmt.Errorf("bash_id is required")
	}
	bgMu.Lock()
	p := bgProcs[in.BashID]
	bgMu.Unlock()
	if p == nil {
		return "", fmt.Errorf("unknown bash_id %q", in.BashID)
	}
	// No done-wait here: a long-running process must not block polling;
	// syncBuffer's lock keeps the read consistent.
	out := p.buf.String()
	if len(out) > bashMaxOutput {
		out = out[:bashMaxOutput] + "…[output capped]"
	}
	bgMu.Lock()
	status := "status: running"
	if p.exited {
		status = fmt.Sprintf("status: done, exit code %d", p.code)
	}
	bgMu.Unlock()
	return out + "\n[" + status + "]", nil
}

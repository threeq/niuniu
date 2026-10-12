package sandbox

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// FenceProbe is one attempted escape and whether the OS refused it.
type FenceProbe struct {
	Name   string `json:"name"`
	Denied bool   `json:"denied"`
	Error  string `json:"error,omitempty"`
}

// FenceReport is the isolation acceptance result for THIS host: the three
// attempts #739 names (read a file, connect a socket directly, start a child
// process), each made from Go inside the box so the answer reflects the OS
// fence rather than the absence of a JS API.
type FenceReport struct {
	Backend   string       `json:"backend"`
	OK        bool         `json:"ok"`
	Probes    []FenceProbe `json:"probes"`
	CheckedAt time.Time    `json:"checked_at"`
}

// FenceSelfTest runs the isolation acceptance against the live fence.
//
// probeFile must be a real file the CURRENT USER can read but the box must not
// (the caller creates it in its own private directory); probeAddr must be a
// listening socket on this machine. Both are supplied by the caller because
// only the caller knows what counts as "readable outside the box" here.
func FenceSelfTest(ctx context.Context, probeFile, probeAddr string) (*FenceReport, error) {
	avail := SelfCheck(false)
	if !avail.Available {
		return nil, fmt.Errorf("cannot run the fence self-test: %s", avail.Reason)
	}
	helper := HelperPath()
	if helper == "" {
		return nil, fmt.Errorf("cannot run the fence self-test: the sandbox helper binary is not configured")
	}
	proc, err := currentFence().Spawn(SpawnRequest{
		Path:        helper,
		Args:        []string{"--fence-selftest", "--file", probeFile, "--addr", probeAddr},
		Env:         boxEnv(),
		Dir:         boxWorkDir(),
		MemoryBytes: 128 << 20,
	})
	if err != nil {
		return nil, fmt.Errorf("spawn the fence self-test box: %w", err)
	}
	defer func() {
		_ = proc.Kill()
		proc.Close()
	}()

	type readResult struct {
		msg ResultMessage
		err error
	}
	done := make(chan readResult, 1)
	go func() {
		br := bufio.NewReader(proc.Stdout())
		kind, body, err := ReadFrame(br)
		if err != nil {
			done <- readResult{err: err}
			return
		}
		if kind != KindResult {
			done <- readResult{err: fmt.Errorf("unexpected frame %q from the self-test box", kind)}
			return
		}
		var m ResultMessage
		if err := json.Unmarshal(body, &m); err != nil {
			done <- readResult{err: err}
			return
		}
		done <- readResult{msg: m}
	}()

	wait := 30 * time.Second
	if dl, ok := ctx.Deadline(); ok {
		if until := time.Until(dl); until < wait {
			wait = until
		}
	}
	select {
	case r := <-done:
		if r.err != nil {
			return nil, fmt.Errorf("read the fence self-test result: %w", r.err)
		}
		rep := &FenceReport{Backend: avail.Backend, CheckedAt: time.Now()}
		out, _ := r.msg.Output.(map[string]any)
		if raw, ok := out["probes"].([]any); ok {
			for _, p := range raw {
				if m, ok := p.(map[string]any); ok {
					probe := FenceProbe{}
					probe.Name, _ = m["name"].(string)
					probe.Denied, _ = m["denied"].(bool)
					probe.Error, _ = m["error"].(string)
					rep.Probes = append(rep.Probes, probe)
				}
			}
		}
		rep.OK = len(rep.Probes) > 0
		for _, p := range rep.Probes {
			if !p.Denied {
				rep.OK = false
			}
		}
		_ = proc.Kill()
		return rep, nil
	case <-time.After(wait):
		_ = proc.Kill()
		return nil, fmt.Errorf("the fence self-test timed out after %s", wait)
	}
}

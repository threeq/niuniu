package agentproxy

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/agentproxy/adapter"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// One-shot turn runtime: the engine-neutral driver for CLIs that process one
// user turn per process invocation (adapter.ProcessOneShot) — Codex's legacy
// `exec` path, Qwen Code and Cursor.
//
// This file owns process orchestration only: spawn, feed the prompt on stdin,
// scan stdout, hand each line to the session's adapter for parsing, bridge
// approval requests to the permission gate, and settle the turn. Everything
// engine-specific lives either in the adapter (parse/argv) or in that engine's
// exec builder (see oneshot_registry.go).
//
// NOTE: this is NOT the same thing as oneshot.go, which serves
// RunOneShotStructured — a throwaway, no-tools, schema-constrained generation
// call used for background analysis. The two are deliberately separate surfaces;
// do not "align" them.
//
// History: this runtime used to live in codex_exec.go, whose name implied it was
// Codex-specific when in fact Qwen and Cursor had always shared it.

func (s *WorkspaceSession) runOneShotTurn(ctx context.Context, workDir, content, msgId string) error {
	command, args, env, err := s.buildOneShotExec(ctx, workDir)
	if err != nil {
		s.finishOneShotTurn(ctx, msgId, true, err.Error())
		return err
	}

	// parseAdapter is resolved once per turn: it parses every stdout line and
	// labels the driver in logs. A nil session adapter falls back to Codex, the
	// same legacy default buildOneShotExec applies — kept consistent so a session
	// cannot parse with one engine's grammar while having been spawned by
	// another's argv. (Previously this fallback was recomputed inside the scan
	// loop for every line, and the driver label above dereferenced s.cliAdapter
	// without the nil guard.)
	parseAdapter := s.cliAdapter
	if parseAdapter == nil {
		parseAdapter = adapter.CodexAdapter{}
	}
	driver := string(parseAdapter.Type())
	slog.Info("agent one-shot: launching exec", "workspaceID", s.workspaceID, "driver", driver, "workDir", workDir, "command", command, "args", args)
	cmdCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, command, args...)
	cmd.Dir = workDir
	cmd.Env = env

	// B2: switch stdin from a one-shot strings.Reader to an io.Pipe so the
	// approval bridge can write approval/response JSON-RPC payloads after the
	// initial prompt. We close the writer end either after the prompt write
	// (when there's no bridge wired) or after cmd.Wait returns (when the
	// bridge may still need to respond mid-turn).
	stdinR, stdinW := io.Pipe()
	cmd.Stdin = stdinR
	// stdinMu guards concurrent writes from the prompt-writer goroutine and
	// the (now-async) approval-response handlers.
	var stdinMu sync.Mutex
	writeStdin := func(b []byte) error {
		stdinMu.Lock()
		defer stdinMu.Unlock()
		_, werr := stdinW.Write(b)
		return werr
	}
	// approvalWG tracks in-flight async approval handlers so we wait for
	// them to write their responses before closing stdinW at turn end.
	var approvalWG sync.WaitGroup

	stderrBuf := newLimitedBuffer(8192)
	cmd.Stderr = stderrBuf
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdinW.Close()
		s.finishOneShotTurn(ctx, msgId, true, "Failed to start agent: "+err.Error())
		return fmt.Errorf("one-shot stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdinW.Close()
		s.finishOneShotTurn(ctx, msgId, true, "Failed to start agent: "+err.Error())
		return fmt.Errorf("one-shot start: %w", err)
	}

	s.procMu.Lock()
	s.cmd = cmd
	s.stdin = nil
	s.stderrBuf = stderrBuf
	s.alive = true
	s.procMu.Unlock()

	// HOTFIX (2026-05-22 post-merge): codex `exec --json` with `-` as input
	// filename reads stdin until EOF, then starts processing. If we keep
	// stdinW open (the original B2 design, to support writing approval/response
	// payloads mid-turn), codex blocks forever on the next read() and never
	// emits any stdout — the scanner.Scan() then blocks forever too, and
	// cmd.Wait never returns. Symptom: 'codex 一直没有返回结果'.
	//
	// Fix: close stdinW immediately after the prompt is written. This restores
	// the pre-B2 EOF semantics (matching strings.NewReader behavior). The
	// trade-off: the B2 approval write-back path becomes best-effort — if
	// codex emits an approval/request notification on stdout, we still parse
	// it and call PermissionService.Request (the SPA card shows up), but the
	// approval/response write to stdin will fail with io.ErrClosedPipe. Codex
	// will then time out the approval internally and abort/deny. This matches
	// the EncodeApprovalResponse doc's "fail closed" promise.
	//
	// Why this is acceptable for now: the default codex policy is
	// approval_policy='never' because niuniu trusts its outer workspace
	// isolation for Codex. Workspaces that explicitly set
	// 'on-request' get a non-functional approval bridge until M2.5.2 confirms
	// codex's real wire protocol for the response channel (codex 0.x app-server
	// may use a separate FIFO / named pipe, not stdin). Async prompt write is
	// kept to handle prompts > 64KB without sync write-stdout deadlock.
	closeStdinOnce := sync.OnceFunc(func() { _ = stdinW.Close() })
	go func() {
		if werr := writeStdin([]byte(content)); werr != nil {
			slog.Warn("agent one-shot: prompt write failed",
				"workspaceID", s.workspaceID, "driver", driver, "err", werr)
		}
		// Close stdin right after the prompt is fully written so codex sees
		// EOF and starts processing. OnceFunc dedupes against the defer.
		closeStdinOnce()
	}()
	defer closeStdinOnce()

	pid := int64(cmd.Process.Pid)
	_ = s.q.UpdateAgentStatus(ctx, store.UpdateAgentStatusParams{
		AgentPid:    sql.NullInt64{Int64: pid, Valid: true},
		AgentStatus: sql.NullString{String: "running", Valid: true},
		ID:          s.workspaceID,
	})

	resultSeen := false
	textStarted := false
	// sawRealBlockStart records whether the adapter emitted a genuine
	// content_block_start. Delta-only streams (legacy codex exec) never do, so
	// the loop synthesizes one; complete Claude-shaped streams (Qwen Code, the
	// sole live user of this runner now that codex uses the app-server) already
	// open the block, so synthesizing again would double the block / leak a
	// stray empty text buffer. Gate the synthesis on its absence.
	sawRealBlockStart := false

	// Inactivity watchdog, the one-shot counterpart of waitForTurnComplete's:
	// this runner blocks on scanner.Scan() with NO turnDone channel and no
	// other watchdog — a wedged process (alive, silent, never exits) would
	// hang the turn forever and queue every later message. The watcher kills
	// the process after the inactivity window; Scan() then returns and the
	// turn fails into the normal error path. Every parsed line (below) bumps
	// lastActivityAt, so a streaming turn is never killed.
	window := s.turnInactivityTimeout
	if window <= 0 {
		window = defaultTurnInactivityTimeout
	}
	s.mu.Lock()
	s.lastActivityAt = time.Now() // baseline: spawn counts as activity
	s.mu.Unlock()
	watchDone := make(chan struct{})
	defer close(watchDone)
	go watchOneShotInactivity(s, cancel, watchDone, window)

	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		s.mu.Lock()
		s.lastActivityAt = time.Now() // feed the one-shot inactivity watchdog
		s.mu.Unlock()
		events, parseErr := parseAdapter.ParseLine(line)
		if parseErr != nil {
			slog.Warn("agent one-shot: parse error", "workspaceID", s.workspaceID, "err", parseErr, "line", truncate(line, 500))
			continue
		}
		for _, ev := range events {
			// B2: approval_request — bridge to PermissionService and write the
			// decision back to codex stdin. Don't forward to handleEvent (which
			// expects niuniu-native event types only).
			//
			// Review fix #3 (M2.5.1): dispatch asynchronously so the scanner
			// keeps draining stdout. PermissionService.Request can block up to
			// 2 hours; a synchronous call would back-pressure codex on stdout
			// (next event sits in the OS pipe buffer) and deadlock on the next
			// approval/request the gate emits before the first one is decided.
			// approvalWG below waits for in-flight approvals at turn end so we
			// don't drop pending responses on the floor.
			if ev.Type == "approval_request" && ev.ApprovalRequest != nil {
				req := ev.ApprovalRequest
				approvalWG.Add(1)
				go func() {
					defer approvalWG.Done()
					s.handleCLIApprovalRequest(ctx, req, writeStdin)
				}()
				continue
			}
			if ev.Type == "stream_event" && ev.StreamEventType == "content_block_start" {
				sawRealBlockStart = true
			}
			if !sawRealBlockStart && ev.Type == "stream_event" && ev.StreamEventType == "content_block_delta" && ev.DeltaType == "text_delta" && !textStarted {
				textStarted = true
				s.handleEvent(ctx, ParsedEvent{Type: "stream_event", StreamEventType: "content_block_start", BlockIndex: 0, BlockType: "text"}, msgId)
			}
			// Codex emits the full assistant message twice within a single turn:
			// once incrementally via agent_message_delta events (the streaming
			// path), and again as a terminal agent_message / item_completed
			// (item.type=agent_message) carrying the assembled text. The delta
			// path has already rendered the content in the SPA via the open
			// content_block; forwarding the terminal assistant event surfaces
			// as the message being printed twice ("你好…你好…"). Drop the
			// terminal copy whenever a text stream block is already open. When
			// codex skips the deltas (some short turns), textStarted stays
			// false and the terminal assistant event passes through as before.
			if ev.Type == "assistant" && textStarted {
				continue
			}
			if ev.Type == "result" {
				// Codex emits multiple result-ish events per turn
				// (turn_completed, task_complete, result, ...); the parser
				// folds them all to Type="result". Forwarding each one paints
				// duplicate Done markers in the SPA chat thread. Keep only
				// the first; ignore the rest until the next turn resets
				// resultSeen.
				if resultSeen {
					continue
				}
				if textStarted {
					s.handleEvent(ctx, ParsedEvent{Type: "stream_event", StreamEventType: "content_block_stop", BlockIndex: 0}, msgId)
					textStarted = false
				}
				resultSeen = true
			}
			s.handleEvent(ctx, ev, msgId)
		}
	}
	if err := scanner.Err(); err != nil {
		slog.Warn("agent one-shot: scanner error", "workspaceID", s.workspaceID, "err", err)
	}

	// Wait for any in-flight approval handlers so the SPA permission card
	// gets a decision recorded (PermissionService writes audit + clears the
	// pending row even when we can't deliver the response back to codex).
	// Note: after the 2026-05-22 hotfix, stdinW is closed right after the
	// prompt write, so the actual write-back is best-effort — see the long
	// comment near the prompt-writer goroutine for why this is acceptable.
	approvalWG.Wait()

	waitErr := cmd.Wait()
	s.procMu.Lock()
	s.alive = false
	s.procMu.Unlock()
	_ = s.q.UpdateAgentStatus(ctx, store.UpdateAgentStatusParams{
		AgentPid:    sql.NullInt64{Valid: false},
		AgentStatus: sql.NullString{String: "idle", Valid: true},
		ID:          s.workspaceID,
	})

	if !resultSeen {
		if textStarted {
			s.handleEvent(ctx, ParsedEvent{Type: "stream_event", StreamEventType: "content_block_stop", BlockIndex: 0}, msgId)
			textStarted = false
		}
		if waitErr != nil {
			msg := strings.TrimSpace(stderrBuf.String())
			if msg == "" {
				msg = waitErr.Error()
			}
			s.finishOneShotTurn(ctx, msgId, true, "Agent failed: "+msg)
			return fmt.Errorf("one-shot exec failed: %w", waitErr)
		}
		s.handleEvent(ctx, ParsedEvent{Type: "result"}, msgId)
	}
	return nil
}

// handleCLIApprovalRequest bridges a CLI approval notification to niuniu's
// PermissionService and writes the driver-specific response payload back on
// stdin. Runs inside the one-shot turn scanner loop; PermissionService.Request
// blocks until the user decides or the timeout fires, mirroring Claude's
// mcp__niuniu_permission_prompt path.
//
// When permissionGate is unset (legacy / test) or returns an error, we deny
// to be safe (the CLI respects the denial; the user sees the failed action in
// the turn output).
//
// Only engines that actually emit approval requests on stdout reach this —
// today that is Codex's `exec` path. Qwen and Cursor auto-approve at the CLI
// boundary instead (--yolo / --force), so they never produce an
// adapter.ApprovalRequest; wiring them to this gate is follow-up work, not a
// silent gap.
func (s *WorkspaceSession) handleCLIApprovalRequest(ctx context.Context, req *adapter.ApprovalRequest, writeStdin func([]byte) error) {
	approved := false
	// Tool names are scoped by CLI driver so allowlist entries from different
	// CLIs never collide. A nil adapter falls back to Codex, matching
	// buildOneShotExec's legacy default.
	cliAdapter := s.cliAdapter
	if cliAdapter == nil {
		cliAdapter = adapter.CodexAdapter{}
	}
	driver := string(cliAdapter.Type())
	if s.permissionGate != nil {
		toolName := driver + ":" + req.Tool
		s.mu.Lock()
		sessionID := s.sessionId
		s.mu.Unlock()
		behavior, err := s.permissionGate.Request(ctx, s.workspaceID, s.ownerType, s.ownerID,
			sessionID, toolName, req.Args)
		if err != nil {
			slog.Warn("agent one-shot approval: PermissionService.Request failed; denying",
				"workspaceID", s.workspaceID, "driver", driver, "tool", req.Tool, "err", err)
		} else if behavior == "allow" {
			approved = true
		}
	} else {
		slog.Warn("agent one-shot approval: no PermissionGate wired; denying",
			"workspaceID", s.workspaceID, "driver", driver, "tool", req.Tool)
	}
	payload, encErr := adapter.EncodeApprovalResponse(req.ID, approved)
	if encErr != nil {
		slog.Warn("agent one-shot approval: encode response failed",
			"workspaceID", s.workspaceID, "driver", driver, "tool", req.Tool, "err", encErr)
		return
	}
	// Newline-terminate so the JSON-RPC framing parser on the CLI's side sees
	// a complete message; the app-server protocol is NDJSON like the stdout
	// stream.
	payload = append(payload, '\n')
	if err := writeStdin(payload); err != nil {
		slog.Warn("agent one-shot approval: write stdin response failed",
			"workspaceID", s.workspaceID, "driver", driver, "tool", req.Tool, "err", err)
	}
}

func (s *WorkspaceSession) finishOneShotTurn(ctx context.Context, msgId string, isError bool, result string) {
	s.handleEvent(ctx, ParsedEvent{
		Type:    "result",
		IsError: isError,
		Result:  result,
	}, msgId)
}

// watchOneShotInactivity kills the one-shot process when the turn has gone
// silent past the watchdog judgment (window + tool grace) — the one-shot
// counterpart of waitForTurnComplete's watchdog (this runner has no turnDone
// channel and no separate waiter; without this, a wedged CLI hung the turn
// forever). The cancel tears down cmdCtx, which kills the process, which EOFs
// the scanner and unblocks the turn into the normal error path.
func watchOneShotInactivity(s *WorkspaceSession, cancel context.CancelFunc, done <-chan struct{}, window time.Duration) {
	watchTurnInactivity(s, cancel, done, window, "agent one-shot")
}

// watchBackendTurnInactivity is the shared watchdog for the protocol-backend
// engines (omp / goose / cursor): identical contract — cancel only when the
// backend has produced no output for the window AND no tool has been in
// flight past its grace ceiling. Replaces the old hard 15-min turn cap that
// killed legitimate long turns mid-flight.
func watchBackendTurnInactivity(s *WorkspaceSession, cancel context.CancelFunc, turnDone <-chan struct{}, window time.Duration) {
	watchTurnInactivity(s, cancel, turnDone, window, "agent backend turn")
}

// watchTurnInactivity is the shared inactivity loop: every tick, judge the
// turn (turnInactivityExceeded — streaming output and in-flight tools extend
// the clock); only a genuinely silent turn past its grace gets cancelled.
func watchTurnInactivity(s *WorkspaceSession, cancel context.CancelFunc, done <-chan struct{}, window time.Duration, label string) {
	ticker := time.NewTicker(window / 5)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if !turnInactivityExceeded(s, toolInactivityGrace, time.Now()) {
				continue // still streaming, or a tool is legitimately running
			}
			s.mu.Lock()
			idle := time.Since(s.lastActivityAt)
			s.mu.Unlock()
			slog.Error(label+": inactivity watchdog — no output within window, killing unresponsive process",
				"workspace_id", s.workspaceID, "idle", idle.String())
			cancel()
			return
		}
	}
}

package agentproxy

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/niuniu-dev/niuniu/internal/agentproxy/adapter"
	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/sceneenv"
	"github.com/niuniu-dev/niuniu/internal/store"
)

func (s *WorkspaceSession) runOneShotTurn(ctx context.Context, workDir, content, msgId string) error {
	command, args, env, err := s.buildOneShotExec(ctx, workDir)
	if err != nil {
		s.finishOneShotTurn(ctx, msgId, true, err.Error())
		return err
	}

	driver := string(s.cliAdapter.Type())
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
			slog.Warn("codex chat: prompt write failed",
				"workspaceID", s.workspaceID, "err", werr)
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
	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		cliAdapter := s.cliAdapter
		if cliAdapter == nil {
			cliAdapter = adapter.CodexAdapter{}
		}
		events, parseErr := cliAdapter.ParseLine(line)
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
// to be safe (codex respects the denial; the user sees the failed action in
// the turn output).
func (s *WorkspaceSession) handleCLIApprovalRequest(ctx context.Context, req *adapter.ApprovalRequest, writeStdin func([]byte) error) {
	approved := false
	if s.permissionGate != nil {
		// Tool names are scoped by CLI driver so allowlist entries from
		// different CLIs never collide.
		cliAdapter := s.cliAdapter
		if cliAdapter == nil {
			cliAdapter = adapter.CodexAdapter{}
		}
		toolName := string(cliAdapter.Type()) + ":" + req.Tool
		s.mu.Lock()
		sessionID := s.sessionId
		s.mu.Unlock()
		behavior, err := s.permissionGate.Request(ctx, s.workspaceID, s.ownerType, s.ownerID,
			sessionID, toolName, req.Args)
		if err != nil {
			slog.Warn("codex approval: PermissionService.Request failed; denying",
				"workspaceID", s.workspaceID, "tool", req.Tool, "err", err)
		} else if behavior == "allow" {
			approved = true
		}
	} else {
		slog.Warn("codex approval: no PermissionGate wired; denying",
			"workspaceID", s.workspaceID, "tool", req.Tool)
	}
	payload, encErr := adapter.EncodeApprovalResponse(req.ID, approved)
	if encErr != nil {
		slog.Warn("codex approval: encode response failed",
			"workspaceID", s.workspaceID, "tool", req.Tool, "err", encErr)
		return
	}
	// Newline-terminate so the JSON-RPC framing parser on codex's side sees
	// a complete message; the app-server protocol is NDJSON like the stdout
	// stream.
	payload = append(payload, '\n')
	if err := writeStdin(payload); err != nil {
		slog.Warn("codex approval: write stdin response failed",
			"workspaceID", s.workspaceID, "tool", req.Tool, "err", err)
	}
}

func (s *WorkspaceSession) buildOneShotExec(ctx context.Context, workDir string) (string, []string, []string, error) {
	oneShotAdapter := s.cliAdapter
	if oneShotAdapter == nil {
		oneShotAdapter = adapter.CodexAdapter{}
	}
	// Qwen Code is one-shot like Codex but shares none of Codex's account /
	// sandbox / config.toml plumbing, so it has its own builder.
	if oneShotAdapter.Type() == adapter.TypeQwen {
		return s.buildQwenOneShotExec(ctx, workDir)
	}
	command := s.cfg.Agent.CodexCli.Command
	if command == "" {
		command = "codex"
	}
	extraArgs := append([]string{}, s.cfg.Agent.CodexCli.Args...)
	model := ""
	wsEnvVars, envErr := sceneenv.Resolve(ctx, s.q, s.workspaceID)
	if envErr != nil {
		return "", nil, nil, fmt.Errorf("fetch workspace env vars: %w", envErr)
	}
	workspaceEnv := make([]adapter.EnvVar, 0, len(wsEnvVars))
	for _, e := range wsEnvVars {
		workspaceEnv = append(workspaceEnv, adapter.EnvVar{Key: e.Key, Value: e.Value})
		switch e.Key {
		case "NIUNIU_AGENT_COMMAND":
			if e.Value != "" {
				command = e.Value
			}
		case "NIUNIU_AGENT_ARGS":
			if e.Value != "" {
				extraArgs = strings.Fields(e.Value)
			}
		case "NIUNIU_MODEL":
			model = e.Value
			if model != "" {
				s.mu.Lock()
				s.modelName = model
				s.mu.Unlock()
			}
		}
	}

	// Resolve per-workspace sandbox + approval settings (B1). Default to full
	// Codex bypass because niuniu provides the outer workspace isolation.
	sandboxMode := "danger-full-access"
	approvalPolicy := "never"
	if cfg, cfgErr := s.q.GetWorkspaceCodexConfig(ctx, s.workspaceID); cfgErr == nil {
		if cfg.CodexSandboxMode != "" {
			sandboxMode = cfg.CodexSandboxMode
		}
		if cfg.CodexApprovalPolicy != "" {
			approvalPolicy = cfg.CodexApprovalPolicy
		}
	} else if !errors.Is(cfgErr, sql.ErrNoRows) {
		slog.Warn("codex chat: failed to read codex config from workspace; using defaults",
			"workspaceID", s.workspaceID, "err", cfgErr)
	}

	s.mu.Lock()
	sessionID := s.sessionId
	s.mu.Unlock()

	var worktreeDirs []string
	worktrees, wtErr := s.q.ListWorktrees(ctx, s.workspaceID)
	if wtErr != nil {
		slog.Warn("codex chat: failed to list worktrees", "workspaceID", s.workspaceID, "err", wtErr)
	} else {
		for _, wt := range worktrees {
			if _, err := os.Stat(wt.WorktreePath); err != nil {
				slog.Warn("codex chat: worktree path missing", "workspaceID", s.workspaceID, "path", wt.WorktreePath, "err", err)
			}
			worktreeDirs = append(worktreeDirs, wt.WorktreePath)
		}
	}

	var mcpOpts *config.MCPGenerateOptions
	if s.mcpWriter != nil {
		projectID, _ := s.q.GetProjectIDForWorkspace(ctx, s.workspaceID)
		opts := config.MCPGenerateOptions{
			ProjectID:           projectID,
			WorkspaceID:         s.workspaceID,
			InboxDir:            filepath.Join(workDir, ".team", "inboxes"),
			SessionToken:        s.sessionToken,
			CodexSandboxMode:    sandboxMode,
			CodexApprovalPolicy: approvalPolicy,
		}
		if mcpArgs, err := s.mcpWriter.GenerateCodexConfigArgs(opts); err != nil {
			slog.Warn("codex chat: build codex mcp config args failed", "workspaceID", s.workspaceID, "err", err)
		} else {
			extraArgs = append(extraArgs, mcpArgs...)
		}
		mcpOpts = &opts
	}

	// Per-account CODEX_HOME switching removed; codex uses the host's global
	// ~/.codex/.
	var accountConfigDir string
	var gitName, gitEmail string
	if gitUID := s.effectiveGitUserID(ctx); s.gitIdentity != nil && gitUID > 0 {
		if name, email, err := s.gitIdentity.ResolveNameEmail(ctx, gitUID); err == nil && name != "" && email != "" {
			gitName, gitEmail = name, email
		}
	}
	env := oneShotAdapter.InjectEnv(os.Environ(), adapter.EnvOptions{
		WorkspaceEnv:     workspaceEnv,
		AccountConfigDir: accountConfigDir,
		GitAuthorName:    gitName,
		GitAuthorEmail:   gitEmail,
	})

	if s.mcpWriter != nil && mcpOpts != nil {
		if err := s.mcpWriter.GenerateCodexConfigToml(workDir, *mcpOpts); err != nil {
			slog.Warn("codex chat: write .codex/config.toml failed", "workspaceID", s.workspaceID, "err", err)
		}
	}
	command, args := oneShotAdapter.BuildSpawn(adapter.SpawnOptions{
		Command:        command,
		ExtraArgs:      extraArgs,
		WorkDir:        workDir,
		SessionID:      sessionID,
		Model:          model,
		WorktreeDirs:   worktreeDirs,
		SandboxMode:    sandboxMode,
		ApprovalPolicy: approvalPolicy,
	})
	return command, args, env, nil
}

func (s *WorkspaceSession) finishOneShotTurn(ctx context.Context, msgId string, isError bool, result string) {
	s.handleEvent(ctx, ParsedEvent{
		Type:    "result",
		IsError: isError,
		Result:  result,
	}, msgId)
}

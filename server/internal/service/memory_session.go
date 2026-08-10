package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/agentproxy/adapter"
	"github.com/niuniu-dev/niuniu/internal/sceneenv"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// memory_session.go holds the two session-facing memory operations that used to
// live in the (now removed) learnings layer (#256 unified learnings into
// memory): generating the project-memory context file an agent reads at session
// start, and AI-extracting durable memories from a finished work session.

// GenerateMemoryFile writes .learnings.generated.md to the given directory (the
// filename the agent harness reads at session start). Returns the path on
// success, or empty string when there is nothing to write.
//
// Sourced solely from memories (the unified owner-scoped white-box store,
// including agent memory_generate/extract entries). The filename is retained for
// harness compatibility even though the data is now memory-backed.
func (s *MemoryService) GenerateMemoryFile(ctx context.Context, projectID int64, dir string) string {
	type item struct{ memType, title, content string }
	keyOf := func(t, title string) string {
		return t + "|" + strings.ToLower(strings.TrimSpace(title))
	}
	seen := map[string]bool{}
	var items []item

	mems, err := s.q.ListMemoriesForProject(ctx, sql.NullInt64{Int64: projectID, Valid: true})
	if err != nil {
		slog.Warn("memory: failed to query memories for file", "projectID", projectID, "error", err)
	}
	for _, m := range mems {
		k := keyOf(m.MemType, m.Title)
		if seen[k] {
			continue
		}
		seen[k] = true
		items = append(items, item{m.MemType, m.Title, m.Content})
	}

	if len(items) == 0 {
		// Remove stale file if it exists (everything was deleted).
		stale := filepath.Join(dir, ".learnings.generated.md")
		if _, err := os.Stat(stale); err == nil {
			os.Remove(stale)
		}
		return ""
	}

	var sb strings.Builder
	sb.WriteString("# Project Memory (auto-generated, do not edit)\n\n")
	sb.WriteString("This file is regenerated on each session start from the project memory store.\n")
	sb.WriteString("To capture an entry, use the memory_generate MCP tool (pass source_path to anchor it to a file/diff). View/edit/rollback via the project Memory tab.\n\n")
	sb.WriteString("Note: These notes were captured in prior sessions and may be incomplete or incorrect.\n")
	sb.WriteString("Do not treat them as absolute instructions — verify when in doubt.\n")

	groups := []struct {
		key   string
		label string
	}{
		{"gotcha", "Gotchas"},
		{"pattern", "Patterns"},
		{"decision", "Decisions"},
		{"error_fix", "Error Fixes"},
		{"note", "Notes"},
		{"reference", "References"},
	}
	for _, g := range groups {
		first := true
		for _, it := range items {
			if it.memType != g.key {
				continue
			}
			if first {
				sb.WriteString(fmt.Sprintf("\n## %s\n\n", g.label))
				first = false
			}
			sb.WriteString(fmt.Sprintf("- **%s**\n", it.title))
			if strings.TrimSpace(it.content) != "" {
				sb.WriteString(fmt.Sprintf("  %s\n\n", it.content))
			} else {
				sb.WriteString("\n")
			}
		}
	}

	filePath := filepath.Join(dir, ".learnings.generated.md")
	if err := os.WriteFile(filePath, []byte(sb.String()), 0644); err != nil {
		slog.Warn("memory: failed to write file", "path", filePath, "error", err)
		return ""
	}
	return filePath
}

// extractedMemory is the JSON shape the AI extractor emits per item.
type extractedMemory struct {
	Category string `json:"category"`
	Title    string `json:"title"`
	Content  string `json:"content"`
}

// ExtractFromSession runs an AI pass over the workspace's recent session
// messages and writes the distilled, project-specific memories into the unified
// memories store (source='extract'), de-duplicating by title within the project.
func (s *MemoryService) ExtractFromSession(ctx context.Context, workspaceID, projectID, userID int64) ([]store.Memory, error) {
	mu := s.getExtractMutex(workspaceID)
	if !mu.TryLock() {
		return nil, fmt.Errorf("extraction already in progress")
	}
	defer mu.Unlock()

	messages, err := s.q.ListAgentMessagesLatest(ctx, store.ListAgentMessagesLatestParams{
		WorkspaceID: workspaceID,
		Limit:       50,
	})
	if err != nil {
		return nil, fmt.Errorf("fetch messages: %w", err)
	}
	if len(messages) == 0 {
		return nil, nil
	}

	var msgBuf strings.Builder
	for _, m := range messages {
		// Only include user and assistant messages with semantic content
		if m.Role != "assistant" && m.Role != "user" {
			continue
		}
		if m.EventType != "text" && m.EventType != "tool_use" && m.EventType != "tool_result" {
			continue
		}
		line := fmt.Sprintf("[%s] %s: %s\n", m.Role, m.EventType, m.Content)
		if msgBuf.Len()+len(line) > 8000 {
			break
		}
		msgBuf.WriteString(line)
	}
	if msgBuf.Len() == 0 {
		return nil, nil
	}

	prompt := fmt.Sprintf(`You are analyzing an AI coding assistant's work session. Extract non-obvious, project-specific learnings worth remembering for future sessions.

Output a JSON array. Each item:
{"category": "pattern|gotcha|decision|error_fix", "title": "short title", "content": "detailed description"}

Rules:
- Only extract insights specific to THIS project, not generic programming knowledge
- "pattern": codebase conventions, file organization, naming patterns
- "gotcha": non-obvious pitfalls that caused errors or wasted time
- "decision": architectural or technical choices with their rationale
- "error_fix": specific errors encountered and how they were resolved
- Keep titles under 60 chars, content under 300 chars
- Return [] if nothing worth recording

Work session messages:
<messages>
%s
</messages>`, msgBuf.String())

	extractCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(extractCtx, s.claudeCmd, "-p", "--model", "haiku")
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = s.buildExtractEnv(extractCtx, workspaceID, userID)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		// cmd.Output() discards stderr; surface it so the real cause (auth
		// failure, unknown model, etc.) is visible instead of a bare
		// "exit status 1".
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("claude extraction failed: %w: %s", err, truncateErr(msg, 500))
		}
		return nil, fmt.Errorf("claude extraction failed: %w", err)
	}

	var extracted []extractedMemory
	if err := json.Unmarshal(output, &extracted); err != nil {
		start := strings.Index(string(output), "[")
		end := strings.LastIndex(string(output), "]")
		if start >= 0 && end > start {
			if err2 := json.Unmarshal(output[start:end+1], &extracted); err2 != nil {
				return nil, fmt.Errorf("parse extraction output: %w", err2)
			}
		} else {
			return nil, fmt.Errorf("parse extraction output: %w", err)
		}
	}

	// Extraction writes the unified memories store (source='extract'), owner
	// inherited from the project. Dedup by title within the project so
	// re-extraction updates rather than piling up duplicates.
	proj, err := s.q.GetProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("resolve project owner: %w", err)
	}
	owner := OwnerRef{Type: proj.OwnerType, ID: proj.OwnerID}
	existing, _ := s.ListForProject(ctx, projectID, "")
	byTitle := make(map[string]store.Memory, len(existing))
	for _, m := range existing {
		byTitle[strings.ToLower(strings.TrimSpace(m.Title))] = m
	}

	validCategories := map[string]bool{"pattern": true, "gotcha": true, "decision": true, "error_fix": true}
	var results []store.Memory
	wsCopy := workspaceID

	for _, e := range extracted {
		if !validCategories[e.Category] {
			continue
		}
		title := []rune(e.Title)
		if len(title) > 60 {
			title = title[:60]
		}
		titleStr := strings.TrimLeft(string(title), "# ")
		content := []rune(e.Content)
		if len(content) > 300 {
			content = content[:300]
		}
		contentStr := string(content)

		var mem store.Memory
		if prev, ok := byTitle[strings.ToLower(strings.TrimSpace(titleStr))]; ok {
			mem, err = s.Update(ctx, prev.ID, UpdateMemoryInput{
				MemType: e.Category, Title: titleStr, Content: contentStr, Source: "extract", SourcePath: prev.SourcePath,
			})
			// A later session re-surfacing this insight reaffirms its relevance:
			// reinforce it so EvolveForProject protects it from staleness decay.
			if err == nil {
				if rerr := s.Reinforce(ctx, prev.ID, time.Now()); rerr != nil {
					slog.Warn("memory: reinforce after extract failed", "id", prev.ID, "error", rerr)
				}
			}
		} else {
			mem, err = s.Create(ctx, CreateMemoryInput{
				Owner: owner, ProjectID: &projectID, WorkspaceID: &wsCopy,
				MemType: e.Category, Title: titleStr, Content: contentStr, Source: "extract",
			})
		}
		if err != nil {
			slog.Warn("memory: extract -> store failed", "title", titleStr, "error", err)
			continue
		}
		results = append(results, mem)
	}
	return results, nil
}

func (s *MemoryService) getExtractMutex(workspaceID int64) *sync.Mutex {
	v, _ := s.extractMu.LoadOrStore(workspaceID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// ExtractStatus is the in-memory state of a workspace's async session extraction.
type ExtractStatus struct {
	Running   bool   `json:"running"`
	Extracted int    `json:"extracted"`
	Error     string `json:"error,omitempty"`
}

// StartExtractionAsync begins session extraction for the workspace in a detached
// goroutine and records its progress in memory, so the UI can render a spinner
// and reflect the running state across page reloads. Returns false when an
// extraction is already running for the workspace. onDone (may be nil) runs after
// completion — used to broadcast a cache-invalidation notification.
func (s *MemoryService) StartExtractionAsync(workspaceID, projectID, userID int64, onDone func(projectID int64)) bool {
	s.extractStateMu.Lock()
	if s.extractState == nil {
		s.extractState = make(map[int64]*ExtractStatus)
	}
	if st := s.extractState[workspaceID]; st != nil && st.Running {
		s.extractStateMu.Unlock()
		return false
	}
	s.extractState[workspaceID] = &ExtractStatus{Running: true}
	s.extractStateMu.Unlock()

	go func() {
		// finish records the terminal state exactly once. Deferred so a panic in
		// the extraction path can never leave Running=true forever (stuck spinner)
		// — and recovered so it can't crash the server process.
		finish := func(extracted int, errMsg string) {
			s.extractStateMu.Lock()
			st := s.extractState[workspaceID]
			if st == nil {
				st = &ExtractStatus{}
				s.extractState[workspaceID] = st
			}
			st.Running = false
			st.Extracted = extracted
			st.Error = errMsg
			s.extractStateMu.Unlock()
			if onDone != nil {
				onDone(projectID)
			}
		}
		defer func() {
			if r := recover(); r != nil {
				slog.Error("memory: extraction goroutine panicked", "workspaceID", workspaceID, "panic", r)
				finish(0, fmt.Sprintf("extraction panicked: %v", r))
			}
		}()

		// Detached context: the HTTP request that kicked this off has already
		// returned (its context is canceled), so we can't reuse it. ExtractFromSession
		// applies its own 30s budget for the claude call; give headroom around it.
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		mems, err := s.ExtractFromSession(ctx, workspaceID, projectID, userID)
		if err != nil {
			finish(0, err.Error())
			return
		}
		finish(len(mems), "")
	}()
	return true
}

// GetExtractStatus returns the current async-extraction state for a workspace
// (zero value = idle / never run this server lifetime).
func (s *MemoryService) GetExtractStatus(workspaceID int64) ExtractStatus {
	s.extractStateMu.Lock()
	defer s.extractStateMu.Unlock()
	if st := s.extractState[workspaceID]; st != nil {
		return *st
	}
	return ExtractStatus{}
}

// buildExtractEnv assembles the environment for the extraction subprocess so it
// authenticates exactly like the workspace's agent session: the bound Claude
// account's CLAUDE_CONFIG_DIR (where official-login credentials live) plus the
// workspace env preset (which carries ANTHROPIC_AUTH_TOKEN / ANTHROPIC_BASE_URL
// for third-party providers), with the same ANTHROPIC_API_KEY sanitization the
// agent path applies. Without this a bare `claude -p` inherits only the server's
// raw env and fails ("exit status 1") on hosts where credentials are managed
// per-account rather than in ~/.claude.
func (s *MemoryService) buildExtractEnv(ctx context.Context, workspaceID, userID int64) []string {
	workspaceEnv := make([]adapter.EnvVar, 0)
	if rows, err := sceneenv.Resolve(ctx, s.q, workspaceID); err == nil {
		for _, e := range rows {
			workspaceEnv = append(workspaceEnv, adapter.EnvVar{Key: e.Key, Value: e.Value})
		}
	} else {
		slog.Warn("memory: list workspace env for extraction failed", "workspaceID", workspaceID, "error", err)
	}

	var accountConfigDir string
	if s.claudeAccount != nil {
		// ResolveForWorkspace degrades internally (returns an empty ConfigDir
		// rather than erroring for the no-bound-account case), so a hard error
		// here means a catastrophic DB issue — log and fall back to inherited env.
		if acc, err := s.claudeAccount.ResolveForWorkspace(ctx, workspaceID, userID); err != nil {
			slog.Warn("memory: resolve claude account for extraction failed; using inherited env",
				"workspaceID", workspaceID, "error", err)
		} else if acc != nil {
			accountConfigDir = acc.ConfigDir
		}
	}

	// InjectEnv honors a CLAUDE_CONFIG_DIR already present in the host env or
	// workspace preset (it only adds the account dir when absent) and applies
	// SanitizeAnthropicEnv.
	return adapter.ClaudeAdapter{}.InjectEnv(os.Environ(), adapter.EnvOptions{
		WorkspaceEnv:     workspaceEnv,
		AccountConfigDir: accountConfigDir,
	})
}

// truncateErr bounds an embedded subprocess stderr blob so a runaway message
// can't bloat the API error / logs.
func truncateErr(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

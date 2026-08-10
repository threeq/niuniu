package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// InboxMessage is the on-disk record written per-recipient. The field shapes
// intentionally match what the legacy niuniu-mcp inbox tool wrote, so existing
// inbox files continue to round-trip through this service.
type InboxMessage struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Text      string `json:"text"`
	Timestamp string `json:"timestamp"`
	MessageID string `json:"messageId"`
	Read      bool   `json:"read"`
}

// InboxService writes per-recipient JSON files compatible with the legacy
// niuniu-mcp inbox tool. Hosting it in the server (vs. a separate MCP process)
// lets us emit team:inbox_sent SSE events for Team-panel monitoring.
type InboxService struct {
	rootDir string
	q       *store.Queries
	bus     *event.Bus
	mu      sync.Mutex
}

// NewInboxService wires an InboxService. rootDir is the on-disk root under
// which per-workspace inbox directories are created (e.g. ~/.niuniu/inbox).
func NewInboxService(rootDir string, q *store.Queries, bus *event.Bus) *InboxService {
	return &InboxService{rootDir: rootDir, q: q, bus: bus}
}

func (s *InboxService) workspaceDir(workspaceID int64) string {
	return filepath.Join(s.rootDir, strconv.FormatInt(workspaceID, 10))
}

// Send writes a new inbox message for `to`. The from agent must be supplied
// by the caller (extracted from X-Niuniu-Agent header by the handler).
//
// Order: emit team:inbox_sent first, then atomic tmp -> rename write of the
// file. If the process crashes between publish and write, the user might see
// an event for a message they later can't read; the spec accepts that since
// the next read attempt still has a reachable file path and Task 17a covers
// the back-fill case.
func (s *InboxService) Send(ctx context.Context, workspaceID int64, from, to, text string) error {
	// Reject any recipient that looks like a path — we never want the MCP to
	// be able to coerce us into writing outside the per-workspace inbox dir.
	// filepath.Base alone is not enough ("../etc" -> "etc") because we need
	// the rejection to surface as an error for the caller.
	if to == "" || to == "." || to == ".." {
		return fmt.Errorf("invalid recipient")
	}
	if strings.ContainsAny(to, `/\`) || strings.Contains(to, "..") {
		return fmt.Errorf("invalid recipient: path separators disallowed")
	}
	if filepath.Base(to) != to {
		return fmt.Errorf("invalid recipient")
	}
	if from == "" {
		return fmt.Errorf("from (sender agent name) required")
	}

	if s.bus != nil {
		s.bus.Publish(event.OutputEvent{
			Type:        event.EventTeamInboxSent,
			Ts:          time.Now().UnixMilli(),
			WorkspaceId: workspaceID,
			InboxSent: &event.InboxSentData{
				WorkspaceID: workspaceID,
				Subject:     truncateInbox(text, 80),
			},
		})
	}

	dir := s.workspaceDir(workspaceID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	msg := InboxMessage{
		From:      from,
		To:        to,
		Text:      text,
		Timestamp: time.Now().Format(time.RFC3339),
		MessageID: strconv.FormatInt(time.Now().UnixNano(), 10),
		Read:      false,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	filePath := filepath.Join(dir, to+".json")
	var messages []InboxMessage
	if data, err := os.ReadFile(filePath); err == nil {
		_ = json.Unmarshal(data, &messages)
	}
	messages = append(messages, msg)
	data, _ := json.MarshalIndent(messages, "", "  ")
	tmpPath := filePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, filePath)
}

// Read returns unread messages for `agent` and marks them read on disk.
func (s *InboxService) Read(ctx context.Context, workspaceID int64, agent string) ([]InboxMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	filePath := filepath.Join(s.workspaceDir(workspaceID), agent+".json")
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var messages []InboxMessage
	if err := json.Unmarshal(data, &messages); err != nil {
		return nil, err
	}
	unread := []InboxMessage{}
	for i := range messages {
		if !messages[i].Read {
			unread = append(unread, messages[i])
			messages[i].Read = true
		}
	}
	updated, _ := json.MarshalIndent(messages, "", "  ")
	_ = os.WriteFile(filePath, updated, 0o644)
	return unread, nil
}

func truncateInbox(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

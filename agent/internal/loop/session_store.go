package loop

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// Session persistence: each session serializes to
// .niuniu-agent/sessions/<id>.json (system + full message history), so a
// headless run can be resumed with -resume and continue with full context.

// SessionState is the serializable snapshot of one session.
type SessionState struct {
	ID       string          `json:"id"`
	System   string          `json:"system"`
	Messages []model.Message `json:"messages"`
	SavedAt  time.Time       `json:"saved_at"`
}

// ExportState snapshots the session for persistence.
func (s *Session) ExportState(id string) *SessionState {
	return &SessionState{
		ID:       id,
		System:   s.system,
		Messages: append([]model.Message(nil), s.messages...),
		SavedAt:  time.Now(),
	}
}

// RestoreSession rebuilds a session from a snapshot: subsequent Prompts
// continue with the full prior context.
func RestoreSession(m model.Model, reg *tools.Registry, st *SessionState) *Session {
	msgs := append([]model.Message(nil), st.Messages...)
	return &Session{m: m, reg: reg, system: st.System, messages: msgs}
}

// SaveSession writes the snapshot to <dir>/<id>.json (overwrites — resuming
// and re-saving updates in place).
func SaveSession(dir string, st *SessionState) error {
	if strings.TrimSpace(st.ID) == "" {
		return errors.New("session id is required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, st.ID+".json"), data, 0o644)
}

// LoadSession reads one snapshot.
func LoadSession(dir, id string) (*SessionState, error) {
	if strings.TrimSpace(id) == "" || strings.ContainsAny(id, `/\`) {
		return nil, fmt.Errorf("invalid session id %q", id)
	}
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		return nil, err
	}
	var st SessionState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse session %s: %w", id, err)
	}
	st.ID = id
	return &st, nil
}

// LatestSessionID returns the id of the most recently saved session.
func LatestSessionID(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var best string
	var bestTime time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if best == "" || info.ModTime().After(bestTime) {
			best = strings.TrimSuffix(e.Name(), ".json")
			bestTime = info.ModTime()
		}
	}
	if best == "" {
		return "", errors.New("no saved sessions")
	}
	return best, nil
}

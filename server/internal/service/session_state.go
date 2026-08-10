package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/git"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// RepoState captures one worktree's git state at session-end time.
type RepoState struct {
	CommitSHA  string   `json:"commit_sha"`
	DirtyHash  string   `json:"dirty_hash"`
	DirtyFiles []string `json:"dirty_files"`
}

// RepoDrift describes a change since the last snapshot for one worktree.
type RepoDrift struct {
	WorktreePath  string   `json:"worktree_path"`
	OldCommit     string   `json:"old_commit"`
	NewCommit     string   `json:"new_commit"`
	CommitsAhead  int      `json:"commits_ahead"`
	NewDirtyFiles []string `json:"new_dirty_files"`
}

// SessionStateService captures and compares workspace git state across
// session boundaries, so that a `claude --resume` can be told what changed
// externally while it was idle.
type SessionStateService struct {
	q *store.Queries
}

func NewSessionStateService(q *store.Queries) *SessionStateService {
	return &SessionStateService{q: q}
}

// CaptureSnapshot computes git state for all worktrees in a workspace and
// upserts a row keyed by (workspaceID, sessionID). lastUserMsg is stored
// (truncated) for the resume banner.
func (s *SessionStateService) CaptureSnapshot(ctx context.Context, workspaceID int64, sessionID string, lastUserMsg string) error {
	if sessionID == "" {
		return nil
	}
	states, err := s.computeRepoStates(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("compute repo states: %w", err)
	}
	payload, err := json.Marshal(states)
	if err != nil {
		return fmt.Errorf("marshal repo states: %w", err)
	}
	return s.q.UpsertSessionState(ctx, store.UpsertSessionStateParams{
		WorkspaceID: workspaceID,
		SessionID:   sessionID,
		RepoStates:  string(payload),
		LastUserMsg: truncate(strings.TrimSpace(lastUserMsg), 200),
	})
}

// DetectDrift compares the current workspace state to the most recent snapshot
// for (workspaceID, sessionID). Returns nil if no snapshot exists or nothing
// changed. Best-effort: errors are returned but not treated as fatal by callers.
func (s *SessionStateService) DetectDrift(ctx context.Context, workspaceID int64, sessionID string) ([]RepoDrift, error) {
	if sessionID == "" {
		return nil, nil
	}
	row, err := s.q.GetSessionState(ctx, store.GetSessionStateParams{
		WorkspaceID: workspaceID,
		SessionID:   sessionID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var stored map[string]RepoState
	if err := json.Unmarshal([]byte(row.RepoStates), &stored); err != nil {
		return nil, fmt.Errorf("unmarshal stored states: %w", err)
	}
	current, err := s.computeRepoStates(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	var drift []RepoDrift
	for path, cur := range current {
		old, ok := stored[path]
		if !ok {
			continue
		}
		if old.CommitSHA == cur.CommitSHA && old.DirtyHash == cur.DirtyHash {
			continue
		}
		var newDirty []string
		if old.DirtyHash != cur.DirtyHash {
			seen := make(map[string]struct{}, len(old.DirtyFiles))
			for _, f := range old.DirtyFiles {
				seen[f] = struct{}{}
			}
			for _, f := range cur.DirtyFiles {
				if _, exists := seen[f]; !exists {
					newDirty = append(newDirty, f)
				}
			}
		}
		d := RepoDrift{
			WorktreePath:  path,
			OldCommit:     old.CommitSHA,
			NewCommit:     cur.CommitSHA,
			NewDirtyFiles: newDirty,
		}
		if old.CommitSHA != cur.CommitSHA && old.CommitSHA != "" && cur.CommitSHA != "" {
			d.CommitsAhead = countCommits(ctx, path, old.CommitSHA, cur.CommitSHA)
		}
		drift = append(drift, d)
	}
	return drift, nil
}

// DriftMessage detects drift and formats it as a human-readable summary,
// suitable for both the chat system_info event and prepending to the next
// user prompt. Returns "" when there's nothing to report.
func (s *SessionStateService) DriftMessage(ctx context.Context, workspaceID int64, sessionID string) (string, error) {
	drift, err := s.DetectDrift(ctx, workspaceID, sessionID)
	if err != nil {
		return "", err
	}
	return FormatDriftMessage(drift), nil
}

// LatestSnapshot returns the stored snapshot row for (workspaceID, sessionID),
// or (zero, false, nil) if not present.
func (s *SessionStateService) LatestSnapshot(ctx context.Context, workspaceID int64, sessionID string) (store.SessionState, bool, error) {
	if sessionID == "" {
		return store.SessionState{}, false, nil
	}
	row, err := s.q.GetSessionState(ctx, store.GetSessionStateParams{
		WorkspaceID: workspaceID,
		SessionID:   sessionID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return store.SessionState{}, false, nil
	}
	if err != nil {
		return store.SessionState{}, false, err
	}
	return row, true, nil
}

func (s *SessionStateService) computeRepoStates(ctx context.Context, workspaceID int64) (map[string]RepoState, error) {
	rows, err := s.q.ListWorktrees(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]RepoState, len(rows))
	for _, w := range rows {
		if w.WorktreePath == "" {
			continue
		}
		sha := commitSHA(ctx, w.WorktreePath)
		files, _ := git.Status(w.WorktreePath)
		names := make([]string, 0, len(files))
		for _, f := range files {
			names = append(names, f.Path)
		}
		sort.Strings(names)
		h := sha256.Sum256([]byte(strings.Join(names, "\x00")))
		capped := names
		if len(capped) > 50 {
			capped = capped[:50]
		}
		out[w.WorktreePath] = RepoState{
			CommitSHA:  sha,
			DirtyHash:  hex.EncodeToString(h[:]),
			DirtyFiles: capped,
		}
	}
	return out, nil
}

// FormatDriftMessage builds a system-style note describing the drift,
// suitable for prepending to the next user prompt AND for displaying in
// the chat UI. Empty result means no drift to report.
func FormatDriftMessage(drift []RepoDrift) string {
	if len(drift) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[niuniu] Files changed externally while you were away:\n")
	for _, d := range drift {
		repo := shortPath(d.WorktreePath)
		switch {
		case d.OldCommit != d.NewCommit && d.CommitsAhead > 0:
			fmt.Fprintf(&b, "- %s: %d new commit(s) (%s → %s)\n", repo, d.CommitsAhead,
				shortSHA(d.OldCommit), shortSHA(d.NewCommit))
		case d.OldCommit != d.NewCommit:
			fmt.Fprintf(&b, "- %s: HEAD changed (%s → %s)\n", repo,
				shortSHA(d.OldCommit), shortSHA(d.NewCommit))
		}
		if len(d.NewDirtyFiles) > 0 {
			preview := d.NewDirtyFiles
			suffix := ""
			if len(preview) > 8 {
				preview = preview[:8]
				suffix = ", …"
			}
			fmt.Fprintf(&b, "- %s: %d new uncommitted change(s) (%s%s)\n",
				repo, len(d.NewDirtyFiles), strings.Join(preview, ", "), suffix)
		}
	}
	b.WriteString("Please reconcile your understanding before proceeding.\n")
	return b.String()
}

func commitSHA(ctx context.Context, worktreePath string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", worktreePath, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func countCommits(ctx context.Context, worktreePath, oldSHA, newSHA string) int {
	out, err := exec.CommandContext(ctx, "git", "-C", worktreePath, "rev-list", "--count", oldSHA+".."+newSHA).Output()
	if err != nil {
		return 0
	}
	n := 0
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &n)
	return n
}

func truncate(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}

func shortSHA(sha string) string {
	if len(sha) < 7 {
		return sha
	}
	return sha[:7]
}

func shortPath(p string) string {
	parts := strings.Split(strings.ReplaceAll(p, "\\", "/"), "/")
	if len(parts) == 0 {
		return p
	}
	return parts[len(parts)-1]
}

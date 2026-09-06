package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// Project 底线 (floor) command — the P1 collapse of the engineering-standards
// config matrix into the single field that is universally applicable.
//
// The harness rule library (harness_specs + column_gate_specs) remains for users
// who want several conditions, but the common case is one question: "what command
// proves this project builds?" That has no sensible default — only the user knows
// it — so it is asked directly instead of being assembled from kind × trigger ×
// applicability. See docs/specs/2026-09-05-harness-engineering-standards-assessment.md.
//
// It lives on projects (floor_command / floor_timeout_sec) rather than as a
// harness_specs row because that table is a global library keyed by
// (category, name) and cannot hold a different command per project.

// maxFloorTimeoutSec caps a floor command's runtime. Ten minutes matches
// floorSpecJobTimeout, the per-check ceiling the floor gate already enforces —
// a larger value here would be silently truncated there.
const maxFloorTimeoutSec = 600

// ProjectFloor is a project's 底线: one shell command that must exit 0 before an
// issue may complete. An empty Command means no floor (completion is not gated).
type ProjectFloor struct {
	Command    string `json:"command"`
	TimeoutSec int    `json:"timeout_sec"`
}

// Active reports whether this floor will actually gate anything.
func (f ProjectFloor) Active() bool { return strings.TrimSpace(f.Command) != "" }

// ProjectFloorService reads and writes the per-project floor command.
// floor_command / floor_timeout_sec are migrate-added columns that are not in
// sqlc, so this uses anchored raw SQL (the established convention for such
// columns — see floor_gate.go / exit_gate.go).
type ProjectFloorService struct {
	db *store.DB
	q  *store.Queries
}

func NewProjectFloorService(db *sql.DB, q *store.Queries) *ProjectFloorService {
	return &ProjectFloorService{db: store.Wrap(db), q: q}
}

// GetFloor returns a project's floor command.
func (s *ProjectFloorService) GetFloor(ctx context.Context, projectID int64) (ProjectFloor, error) {
	var f ProjectFloor
	err := s.db.QueryRowContext(ctx,
		`SELECT floor_command, floor_timeout_sec FROM projects WHERE id = ?`,
		projectID).Scan(&f.Command, &f.TimeoutSec)
	if err != nil {
		return ProjectFloor{}, err
	}
	return f, nil
}

// ProjectFloorSummary is one project's floor in the cross-project overview shown
// on the engineering-standards page. The embedded ProjectFloor flattens into the
// same JSON shape GetFloor returns, plus the owning project's id.
type ProjectFloorSummary struct {
	ProjectID int64 `json:"project_id"`
	ProjectFloor
}

// ListFloors returns the floors of the given projects, skipping ids that do not
// exist. Callers are responsible for having authorized every id first — this is
// a plain read keyed by the ids it is handed.
func (s *ProjectFloorService) ListFloors(ctx context.Context, ids []int64) ([]ProjectFloorSummary, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	// Build the IN placeholder list; ids are int64 so there is nothing to escape.
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, floor_command, floor_timeout_sec FROM projects WHERE id IN (`+
			strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ProjectFloorSummary, 0, len(ids))
	for rows.Next() {
		var f ProjectFloorSummary
		if err := rows.Scan(&f.ProjectID, &f.Command, &f.TimeoutSec); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// SetFloor stores a project's floor command. A blank command clears the floor.
// The timeout is defaulted when unset and capped at maxFloorTimeoutSec so a value
// the floor gate could not honour is never persisted.
func (s *ProjectFloorService) SetFloor(ctx context.Context, projectID int64, f ProjectFloor) error {
	cmd := strings.TrimSpace(f.Command)
	timeout := f.TimeoutSec
	if cmd == "" {
		// Clearing the floor: normalize the timeout back to the default so a
		// re-enable does not inherit a stale value.
		timeout = defaultFloorCommandTimeoutSec
	}
	if timeout <= 0 {
		timeout = defaultFloorCommandTimeoutSec
	}
	if timeout > maxFloorTimeoutSec {
		return fmt.Errorf("timeout_sec must be at most %d seconds", maxFloorTimeoutSec)
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE projects SET floor_command = ?, floor_timeout_sec = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		cmd, timeout, projectID)
	return err
}

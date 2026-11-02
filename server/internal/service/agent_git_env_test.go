package service

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Which user's git credentials a workspace gets. Workspaces that never recorded
// a session user (fresh, scheduler-driven, or opened by another member) used to
// fall through to "no credentials at all" — every in-workspace `git pull` then
// failed with "could not read Username" even though the creator had one stored.
func TestGitEnvUserID(t *testing.T) {
	null := sql.NullInt64{}
	valid := func(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }

	cases := []struct {
		name      string
		session   sql.NullInt64
		createdBy sql.NullInt64
		want      int64
	}{
		{"session user wins", valid(1), valid(2), 1},
		{"falls back to creator", null, valid(2), 2},
		{"zero session falls back", valid(0), valid(2), 2},
		{"nothing recorded", null, null, 0},
		{"zero creator", null, valid(0), 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, gitEnvUserID(c.session, c.createdBy))
		})
	}
}

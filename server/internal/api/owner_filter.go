package api

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// OwnerFilter is a parsed `?owner=` query value. Empty Type means no filter
// — the caller should pass everything through.
//
// Format the frontend sends:
//   - "user:<id>"   — match resources owned by user with that id
//   - "org:<slug>"  — match resources owned by the org with that slug;
//     the slug is resolved to an id via GetOrganizationBySlug.
//
// The list endpoints (/api/projects, /api/workspaces, /api/repositories)
// were silently ignoring this until the org detail "资源" tab made the bug
// visible — every org card showed the caller's full accessible set instead
// of just that org's rows.
type OwnerFilter struct {
	Type string // "" | "user" | "org"
	ID   int64
}

// Match returns true when (t,id) is a member of the filter (or no filter
// is set).
func (f OwnerFilter) Match(t string, id int64) bool {
	if f.Type == "" {
		return true
	}
	return f.Type == t && f.ID == id
}

// ParseOwnerFilter reads ?owner= from the request and resolves it. Returns
// an empty filter (no-op Match) when the param is missing.
//
// db may be nil — in that case org-by-slug lookups can't be done and an
// "org:<slug>" filter is rejected. The list handlers all carry a *sql.DB
// already, so callers should always pass it through.
func ParseOwnerFilter(c *gin.Context, db *sql.DB) (OwnerFilter, error) {
	raw := strings.TrimSpace(c.Query("owner"))
	if raw == "" {
		return OwnerFilter{}, nil
	}
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 || parts[1] == "" {
		return OwnerFilter{}, fmt.Errorf("invalid owner filter %q (expected user:<id> or org:<slug>)", raw)
	}
	switch parts[0] {
	case "user":
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return OwnerFilter{}, fmt.Errorf("invalid user id %q", parts[1])
		}
		return OwnerFilter{Type: "user", ID: id}, nil
	case "org":
		// Numeric → treat as org id directly. Covers SPA paths where
		// project.owner.slug is missing/empty (the frontend's
		// listRepositories falls back to o.id, producing "org:<id>"); the
		// strict slug-only path here used to return "org not found: <id>"
		// and broke the project-settings → associated-repos picker. If the
		// id doesn't correspond to a real org, the downstream list query
		// returns empty — that's the right user-visible outcome.
		if id, parseErr := strconv.ParseInt(parts[1], 10, 64); parseErr == nil && id > 0 {
			return OwnerFilter{Type: "org", ID: id}, nil
		}
		if db == nil {
			return OwnerFilter{}, fmt.Errorf("org slug filter requires a database handle")
		}
		q := store.Wrap(db).Queries()
		org, err := q.GetOrganizationBySlug(c.Request.Context(), parts[1])
		if err != nil {
			return OwnerFilter{}, fmt.Errorf("org not found: %s", parts[1])
		}
		return OwnerFilter{Type: "org", ID: org.ID}, nil
	default:
		return OwnerFilter{}, fmt.Errorf("invalid owner filter type %q (expected user or org)", parts[0])
	}
}

// silence unused-import warnings if the tooling re-orders things.
var _ = context.Background

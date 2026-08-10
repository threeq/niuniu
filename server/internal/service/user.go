package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// UserService backs user-discovery flows used by org-member management.
type UserService struct {
	q     *store.Queries
	db    *store.DB
	authz *Authz
}

// SearchResultUser is the minimal projection returned to org admins.
type SearchResultUser struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

// SearchUsersResult bundles the LIMIT-20 page with the total match count.
type SearchUsersResult struct {
	Users []SearchResultUser
	Total int64
}

func NewUserService(q *store.Queries, db *sql.DB, authz *Authz) *UserService {
	return &UserService{q: q, db: store.Wrap(db), authz: authz}
}

// SearchForOrg returns up to 20 users whose username or display_name matches q
// (ASCII case-insensitive, prefix-preferred), excluding users already in orgID.
// Caller must be owner or admin of orgID.
func (s *UserService) SearchForOrg(ctx context.Context, callerUserID, orgID int64, q string) (SearchUsersResult, error) {
	if err := s.authz.CanManageOrg(ctx, callerUserID, orgID); err != nil {
		return SearchUsersResult{}, err
	}
	q = strings.TrimSpace(q)
	if q == "" {
		return SearchUsersResult{}, errors.New("q must not be empty")
	}
	if len(q) > 50 {
		return SearchUsersResult{}, errors.New("q must be <= 50 chars")
	}
	// SQL has no ESCAPE clause (sqlc parser limitation; see spec §3.1 / §10).
	// User-input wildcards %, _ will match as SQL wildcards — accepted per spec.
	// sqlc field names are positional: 1st ? = prefix (CASE WHEN), 2nd = org_id,
	// 3rd = like (WHERE). The two LOWER-wrapped slots get sqlc-default names
	// LOWER and LOWER_2 because we couldn't use sqlc.arg() (the *store.DB
	// pg-rewriter mangles ?N → $N1$N).
	rows, err := s.q.SearchUsersForOrg(ctx, store.SearchUsersForOrgParams{
		LOWER:   q + "%",
		OrgID:   orgID,
		LOWER_2: "%" + q + "%",
	})
	if err != nil {
		return SearchUsersResult{}, fmt.Errorf("search users: %w", err)
	}
	out := SearchUsersResult{
		Users: make([]SearchResultUser, 0, len(rows)),
	}
	for _, r := range rows {
		out.Users = append(out.Users, SearchResultUser{
			ID:          r.ID,
			Username:    r.Username,
			DisplayName: r.DisplayName,
		})
		out.Total = r.Total // every row carries the same total
	}
	return out, nil
}

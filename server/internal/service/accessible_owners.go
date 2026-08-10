package service

import (
	"context"
)

// AccessibleOwners holds the set of org IDs that a user belongs to.
// Personal access is always implicit (the user owns their own resources directly).
type AccessibleOwners struct {
	UserID int64
	OrgIDs []int64
}

// Accessible returns the orgs the caller belongs to, using a 5-second LRU cache.
// On a cache hit the cached pointer is returned directly (callers must not mutate it).
func (a *Authz) Accessible(ctx context.Context, userID int64) (*AccessibleOwners, error) {
	if v, ok := a.cache.Get(userID); ok {
		return v, nil
	}

	rows, err := a.db.QueryContext(ctx,
		`SELECT org_id FROM org_members WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orgIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		orgIDs = append(orgIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	v := &AccessibleOwners{UserID: userID, OrgIDs: orgIDs}
	a.cache.Put(userID, v)
	return v, nil
}

// containsOrgID returns true if orgID is in owners.OrgIDs.
func containsOrgID(owners *AccessibleOwners, orgID int64) bool {
	for _, id := range owners.OrgIDs {
		if id == orgID {
			return true
		}
	}
	return false
}

// checkAccess enforces ownership: personal resources require owner.ID==userID;
// org resources require the caller to be a member of the org.
func (a *Authz) checkAccess(ctx context.Context, userID int64, owner OwnerRef) error {
	if owner.Type == "user" {
		if owner.ID != userID {
			return ErrForbidden
		}
		return nil
	}
	// owner.Type == "org"
	owners, err := a.Accessible(ctx, userID)
	if err != nil {
		return err
	}
	if !containsOrgID(owners, owner.ID) {
		return ErrForbidden
	}
	return nil
}

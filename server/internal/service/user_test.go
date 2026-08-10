package service

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupUserSearchFixture creates a fresh DB, an owner, and an org owned by them.
func setupUserSearchFixture(t *testing.T) (svc *UserService, orgSvc *OrgService, ownerID, orgID int64, db *sql.DB) {
	t.Helper()
	db = openOrgTestDB(t)
	q := store.New(db)
	authz := NewAuthz(q, db)
	orgSvc = NewOrgService(q, db, authz)
	svc = NewUserService(q, db, authz)

	ownerID = newOrgTestUser(t, db, "owner", "admin")
	org, err := orgSvc.Create(context.Background(), ownerID, "owner", "Acme", "acme", "")
	require.NoError(t, err)
	return svc, orgSvc, ownerID, org.ID, db
}

func TestUserService_SearchForOrg_ExcludesExistingMembers(t *testing.T) {
	svc, orgSvc, ownerID, orgID, db := setupUserSearchFixture(t)

	newOrgTestUser(t, db, "alice", "member")
	newOrgTestUser(t, db, "alex", "member")
	bobID := newOrgTestUser(t, db, "alfred", "member")
	require.NoError(t, orgSvc.AddMember(context.Background(), ownerID, "owner", orgID, bobID, "member"))

	out, err := svc.SearchForOrg(context.Background(), ownerID, orgID, "al")
	require.NoError(t, err)
	usernames := make([]string, 0, len(out.Users))
	for _, u := range out.Users {
		usernames = append(usernames, u.Username)
	}
	assert.ElementsMatch(t, []string{"alice", "alex"}, usernames)
}

func TestUserService_SearchForOrg_PrefixOrdering(t *testing.T) {
	svc, _, ownerID, orgID, db := setupUserSearchFixture(t)
	newOrgTestUser(t, db, "alex_dev", "member")
	newOrgTestUser(t, db, "xalex", "member")

	out, err := svc.SearchForOrg(context.Background(), ownerID, orgID, "al")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(out.Users), 2)
	idxAlex, idxXalex := -1, -1
	for i, u := range out.Users {
		if u.Username == "alex_dev" {
			idxAlex = i
		}
		if u.Username == "xalex" {
			idxXalex = i
		}
	}
	require.NotEqual(t, -1, idxAlex)
	require.NotEqual(t, -1, idxXalex)
	assert.Less(t, idxAlex, idxXalex)
}

func TestUserService_SearchForOrg_LimitsTo20AndReturnsTotal(t *testing.T) {
	svc, _, ownerID, orgID, db := setupUserSearchFixture(t)
	for i := 0; i < 25; i++ {
		newOrgTestUser(t, db, fmt.Sprintf("user%02d", i), "member")
	}
	out, err := svc.SearchForOrg(context.Background(), ownerID, orgID, "user")
	require.NoError(t, err)
	assert.Equal(t, 20, len(out.Users))
	assert.Equal(t, int64(25), out.Total)
}

func TestUserService_SearchForOrg_OwnerSelfFiltered(t *testing.T) {
	svc, _, ownerID, orgID, _ := setupUserSearchFixture(t)
	out, err := svc.SearchForOrg(context.Background(), ownerID, orgID, "owner")
	require.NoError(t, err)
	assert.Empty(t, out.Users)
	assert.Equal(t, int64(0), out.Total)
}

func TestUserService_SearchForOrg_AuthzRejectsNonAdmin(t *testing.T) {
	svc, orgSvc, ownerID, orgID, db := setupUserSearchFixture(t)
	memberID := newOrgTestUser(t, db, "regular", "member")
	require.NoError(t, orgSvc.AddMember(context.Background(), ownerID, "owner", orgID, memberID, "member"))

	_, err := svc.SearchForOrg(context.Background(), memberID, orgID, "any")
	assert.ErrorIs(t, err, ErrForbidden)

	strangerID := newOrgTestUser(t, db, "stranger", "member")
	_, err = svc.SearchForOrg(context.Background(), strangerID, orgID, "any")
	assert.ErrorIs(t, err, ErrForbidden)

	_, err = svc.SearchForOrg(context.Background(), ownerID, orgID, "regular")
	assert.NoError(t, err)
}

func TestUserService_SearchForOrg_EmptyQuery(t *testing.T) {
	svc, _, ownerID, orgID, _ := setupUserSearchFixture(t)
	_, err := svc.SearchForOrg(context.Background(), ownerID, orgID, "")
	assert.Error(t, err)
	_, err = svc.SearchForOrg(context.Background(), ownerID, orgID, "    ")
	assert.Error(t, err)
}

func TestUserService_SearchForOrg_CaseInsensitiveASCII(t *testing.T) {
	svc, _, ownerID, orgID, db := setupUserSearchFixture(t)
	newOrgTestUser(t, db, "Alice", "member")

	out, err := svc.SearchForOrg(context.Background(), ownerID, orgID, "ALI")
	require.NoError(t, err)
	require.NotEmpty(t, out.Users)
	assert.Equal(t, "Alice", out.Users[0].Username)
}

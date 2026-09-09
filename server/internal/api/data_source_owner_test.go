// Owner-config tests: creating a data source under an ORG owner and the
// team-edition visibility contract. Regression for the "settings-created
// sources were hardwired to the caller's personal owner, and the project
// association endpoints passed orgIDs=nil so org-owned sources answered
// 'not found' for everyone" gap.
package api_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/dataconn"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ownerFixture wires the handler WITH Authz (org membership checks are live)
// plus the project-association routes, and seeds an org with two members:
// OwnerA (org owner) and MemberB (plain member) plus an org-owned project.
type ownerFixture struct {
	r       *gin.Engine
	db      *sql.DB
	q       *store.Queries
	ownerA  int64
	memberB int64
	orgID   int64
	project int64
}

func setupOwnerRouter(t *testing.T) *ownerFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, q := niutest.SetupDBRaw(t)
	ctx := context.Background()

	uA, err := q.CreateUser(ctx, store.CreateUserParams{
		Username: "ownerA", PasswordHash: "x", DisplayName: "Owner A", Role: "member",
	})
	require.NoError(t, err)
	uB, err := q.CreateUser(ctx, store.CreateUserParams{
		Username: "memberB", PasswordHash: "x", DisplayName: "Member B", Role: "member",
	})
	require.NoError(t, err)
	org, err := q.CreateOrganization(ctx, store.CreateOrganizationParams{
		Slug: "team", Name: "Team", Description: "", CreatedBy: uA.ID,
	})
	require.NoError(t, err)
	require.NoError(t, q.AddOrgMember(ctx, store.AddOrgMemberParams{OrgID: org.ID, UserID: uA.ID, Role: "owner"}))
	require.NoError(t, q.AddOrgMember(ctx, store.AddOrgMemberParams{OrgID: org.ID, UserID: uB.ID, Role: "member"}))

	proj, err := q.CreateProject(ctx, store.CreateProjectParams{
		Name: "team-proj", Description: sql.NullString{},
		OwnerType: "org", OwnerID: org.ID, Color: sql.NullString{String: "#000000", Valid: true},
	})
	require.NoError(t, err)

	kr, err := crypto.LoadOrCreate(t.TempDir() + "/integration_secret")
	require.NoError(t, err)
	svc := service.NewDataSourceService(q, db, kr, dataconn.NewRegistry())
	h := api.NewDataSourceHandler(svc, dataconn.NewPool())
	h.Authz = service.NewAuthz(q, db)

	r := gin.New()
	r.Use(func(c *gin.Context) {
		if v := c.GetHeader("X-Test-Caller-ID"); v != "" {
			c.Set("auth_user_id", atoi64(v))
		}
		c.Next()
	})
	r.GET("/api/me/data-sources", h.List)
	r.POST("/api/me/data-sources", h.Create)
	r.GET("/api/projects/:id/data-sources", h.ListProjectSources)
	r.POST("/api/projects/:id/data-sources", h.AddProjectSource)

	return &ownerFixture{
		r: r, db: db, q: q,
		ownerA: uA.ID, memberB: uB.ID, orgID: org.ID, project: proj.ID,
	}
}

// createOrgSource creates an org-owned source via the API as OwnerA.
func createOrgSource(t *testing.T, f *ownerFixture, name string) int64 {
	t.Helper()
	w := dsReq(t, f.r, http.MethodPost, "/api/me/data-sources",
		`{"name":"`+name+`","kind":"mysql","config":{"host":"db","port":3306},"owner_type":"org","owner_id":`+itoa(f.orgID)+`}`,
		f.ownerA)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var created map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	return int64(created["id"].(float64))
}

// TestDataSourceHandler_CreateOrgOwned pins owner config on create: an org
// member can create a source under the org's owner, and the row is visible in
// the settings lists of BOTH members (orgIDs now flow through List).
func TestDataSourceHandler_CreateOrgOwned(t *testing.T) {
	f := setupOwnerRouter(t)

	w := dsReq(t, f.r, http.MethodPost, "/api/me/data-sources",
		`{"name":"team-db","kind":"mysql","config":{"host":"db","port":3306},"owner_type":"org","owner_id":`+itoa(f.orgID)+`}`,
		f.ownerA)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var created map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	assert.Equal(t, "org", created["owner_type"])
	assert.Equal(t, itoa(f.orgID), itoa(int64(created["owner_id"].(float64))))

	// Both members see the org source in their settings list.
	for _, uid := range []int64{f.ownerA, f.memberB} {
		w = dsReq(t, f.r, http.MethodGet, "/api/me/data-sources", "", uid)
		require.Equal(t, http.StatusOK, w.Code)
		var got map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
		items := got["items"].([]any)
		require.Len(t, items, 1, "member %d must see the org source", uid)
		assert.Equal(t, "team-db", items[0].(map[string]any)["name"])
	}
}

// TestDataSourceHandler_CreateOrgRequiresMembership: a user OUTSIDE the org
// cannot create a source under it.
func TestDataSourceHandler_CreateOrgRequiresMembership(t *testing.T) {
	f := setupOwnerRouter(t)
	outsider := f.memberB + 999 // created users are sequential; far enough to be outside
	w := dsReq(t, f.r, http.MethodPost, "/api/me/data-sources",
		`{"name":"sneaky","kind":"mysql","config":{"host":"db"},"owner_type":"org","owner_id":`+itoa(f.orgID)+`}`,
		outsider)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

// TestDataSourceHandler_CreatePersonalOwnerMismatch: an explicit PERSONAL
// owner that is not the caller is rejected (cannot create sources "for"
// another user).
func TestDataSourceHandler_CreatePersonalOwnerMismatch(t *testing.T) {
	f := setupOwnerRouter(t)
	w := dsReq(t, f.r, http.MethodPost, "/api/me/data-sources",
		`{"name":"impersonal","kind":"mysql","config":{"host":"db"},"owner_type":"user","owner_id":`+itoa(f.memberB)+`}`,
		f.ownerA)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

// TestDataSourceHandler_ProjectPanelSeesOrgSources pins the orgIDs fix on the
// association panel: an org-owned source IS listed for member B and CAN be
// bound by them to the org project. Before the fix both endpoints passed
// orgIDs=nil, so every org source answered 404 "not found".
func TestDataSourceHandler_ProjectPanelSeesOrgSources(t *testing.T) {
	f := setupOwnerRouter(t)
	id := createOrgSource(t, f, "team-db")

	// Member B binds the org source to the org project (write as a plain
	// member — mirrors EnsureOwnerWritable's any-membership-can-create rule).
	w := dsReq(t, f.r, http.MethodPost, "/api/projects/"+itoa(f.project)+"/data-sources",
		`{"source_id":`+itoa(id)+`}`, f.memberB)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	// The panel now lists it for member B.
	w = dsReq(t, f.r, http.MethodGet, "/api/projects/"+itoa(f.project)+"/data-sources", "", f.memberB)
	require.Equal(t, http.StatusOK, w.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	items := got["items"].([]any)
	require.Len(t, items, 1)
	assert.Equal(t, "team-db", items[0].(map[string]any)["name"])
}

// TestDataSourceHandler_PersonalSourceStaysPersonal pins the boundary: member
// B cannot bind (or even see) OWNER A's PERSONAL source — the owner-config
// feature must not leak personal sources into the team.
func TestDataSourceHandler_PersonalSourceStaysPersonal(t *testing.T) {
	f := setupOwnerRouter(t)
	w := dsReq(t, f.r, http.MethodPost, "/api/me/data-sources",
		`{"name":"private-db","kind":"mysql","config":{"host":"db"}}`, f.ownerA)
	require.Equal(t, http.StatusCreated, w.Code)
	var created map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	id := int64(created["id"].(float64))

	w = dsReq(t, f.r, http.MethodPost, "/api/projects/"+itoa(f.project)+"/data-sources",
		`{"source_id":`+itoa(id)+`}`, f.memberB)
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())

	w = dsReq(t, f.r, http.MethodGet, "/api/projects/"+itoa(f.project)+"/data-sources", "", f.memberB)
	require.Equal(t, http.StatusOK, w.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Empty(t, got["items"].([]any), "personal source must not appear in member B's panel")
}

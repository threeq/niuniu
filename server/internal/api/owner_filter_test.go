package api_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/store"
	testutil "github.com/niuniu-dev/niuniu/internal/testing"
)

func newOwnerCtx(t *testing.T, raw string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if raw == "" {
		c.Request = httptest.NewRequest("GET", "/", nil)
	} else {
		c.Request = httptest.NewRequest("GET", "/?owner="+raw, nil)
	}
	return c
}

// TestParseOwnerFilter_OrgNumericFallback verifies that "org:<numeric-id>"
// is accepted as a direct id reference. Guards against the SPA gap where
// project.owner.slug is missing/empty and the frontend's listRepositories
// falls back to o.id, producing "org:31" — the strict slug-only path
// returned "org not found: 31" and broke the project settings → associated
// repos picker.
func TestParseOwnerFilter_OrgNumericFallback(t *testing.T) {
	got, err := api.ParseOwnerFilter(newOwnerCtx(t, "org%3A31"), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Type != "org" || got.ID != 31 {
		t.Fatalf("got %+v, want {org, 31}", got)
	}
}

func TestParseOwnerFilter_UserNumeric(t *testing.T) {
	got, err := api.ParseOwnerFilter(newOwnerCtx(t, "user%3A7"), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Type != "user" || got.ID != 7 {
		t.Fatalf("got %+v, want {user, 7}", got)
	}
}

func TestParseOwnerFilter_OrgSlug(t *testing.T) {
	db := testutil.SetupTestDB(t)
	q := store.New(db)
	ctx := context.Background()
	if _, err := q.CreateOrganization(ctx, store.CreateOrganizationParams{
		Slug:      "acme",
		Name:      "Acme",
		CreatedBy: 1,
	}); err != nil {
		t.Fatalf("seed org: %v", err)
	}

	got, err := api.ParseOwnerFilter(newOwnerCtx(t, "org%3Aacme"), db)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Type != "org" || got.ID == 0 {
		t.Fatalf("got %+v, want {org, <non-zero-id>}", got)
	}
}

func TestParseOwnerFilter_Empty(t *testing.T) {
	got, err := api.ParseOwnerFilter(newOwnerCtx(t, ""), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Type != "" {
		t.Fatalf("expected empty filter, got %+v", got)
	}
}

func TestParseOwnerFilter_Malformed(t *testing.T) {
	if _, err := api.ParseOwnerFilter(newOwnerCtx(t, "foo"), nil); err == nil {
		t.Fatal("expected error for malformed owner, got nil")
	}
}

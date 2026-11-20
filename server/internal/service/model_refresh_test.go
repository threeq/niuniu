package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
)

// newRefreshFixture builds a service against a fresh in-memory DB with the
// builtin models seeded, plus one provider whose base_urls map is given. A
// non-empty rawKey is stored in an account and referenced through the provider,
// exercising the real ${ACCOUNT:<name>} resolution path.
func newRefreshFixture(t *testing.T, baseURLs map[string]string, rawKey string) (*ModelRefreshService, *ModelLibraryService, int64) {
	t.Helper()
	ctx := context.Background()
	db := setupModelSeedTestDB(t)
	q := store.New(db)
	require.NoError(t, SeedBuiltinModels(ctx, q))

	apiKey := ""
	if rawKey != "" {
		_, err := q.CreateEnvAccount(ctx, store.CreateEnvAccountParams{
			Name:      "refresh-acct",
			Platform:  "test",
			ApiKey:    rawKey,
			OwnerType: "user",
			OwnerID:   1,
		})
		require.NoError(t, err)
		apiKey = "${ACCOUNT:refresh-acct}"
	}
	encoded, err := json.Marshal(baseURLs)
	require.NoError(t, err)
	p, err := q.CreateEnvProvider(ctx, store.CreateEnvProviderParams{
		Name:      "refresh-provider",
		BaseUrls:  string(encoded),
		ApiKey:    apiKey,
		Enabled:   1,
		OwnerType: "user",
		OwnerID:   1,
	})
	require.NoError(t, err)

	lib := NewModelLibraryService(q)
	return NewModelRefreshService(q, lib), lib, p.ID
}

func candidateNames(list []ModelCandidate) []string {
	out := make([]string, len(list))
	for i, c := range list {
		out[i] = c.Name
	}
	return out
}

// TestRefreshPreviewTriesOpenAIThenCodexThenAnthropic pins the protocol order:
// a 404 on openai moves to codex, whose 200 stops the search even though an
// anthropic base_url is also configured.
func TestRefreshPreviewTriesOpenAIThenCodexThenAnthropic(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/openai/models":
			http.Error(w, "not found", http.StatusNotFound)
		case "/codex/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"glm-5.1"},{"id":"codex-only-model"},{"id":"glm-5.1"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	svc, _, providerID := newRefreshFixture(t, map[string]string{
		"openai":    srv.URL + "/openai",
		"codex":     srv.URL + "/codex",
		"anthropic": srv.URL + "/anthropic",
	}, "")
	got, err := svc.RefreshPreview(context.Background(), OwnerRef{Type: "user", ID: 1}, providerID)
	require.NoError(t, err)
	require.Equal(t, []string{"codex-only-model", "glm-5.1"}, candidateNames(got))
	require.Equal(t, []string{"/openai/models", "/codex/models"}, paths)
}

// TestRefreshPreviewAnthropicSendsBothAuthHeaders covers the last protocol in
// the chain: it must carry Bearer plus x-api-key and anthropic-version, and it
// parses models[].name when no id is present.
func TestRefreshPreviewAnthropicSendsBothAuthHeaders(t *testing.T) {
	var gotAuth, gotKey, gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		_, _ = w.Write([]byte(`{"models":[{"name":"anthropic-model-b"},{"id":"anthropic-model-a"}]}`))
	}))
	defer srv.Close()

	svc, _, providerID := newRefreshFixture(t, map[string]string{
		"anthropic": srv.URL + "/anthropic",
	}, "sk-test-key")
	got, err := svc.RefreshPreview(context.Background(), OwnerRef{Type: "user", ID: 1}, providerID)
	require.NoError(t, err)
	require.Equal(t, []string{"anthropic-model-a", "anthropic-model-b"}, candidateNames(got))
	require.Equal(t, "Bearer sk-test-key", gotAuth)
	require.Equal(t, "sk-test-key", gotKey)
	require.Equal(t, "2023-06-01", gotVersion)
}

// TestRefreshPreviewParsesDataName covers the third documented shape:
// data[].name (no id).
func TestRefreshPreviewParsesDataName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"name":"named-model"}]}`))
	}))
	defer srv.Close()

	svc, _, providerID := newRefreshFixture(t, map[string]string{
		"openai": srv.URL + "/openai",
	}, "")
	got, err := svc.RefreshPreview(context.Background(), OwnerRef{Type: "user", ID: 1}, providerID)
	require.NoError(t, err)
	require.Equal(t, []string{"named-model"}, candidateNames(got))
}

// TestRefreshPreviewMarksAlreadyInLibrary verifies the preview flag: builtin or
// owner rows are reported as already present, new names are not.
func TestRefreshPreviewMarksAlreadyInLibrary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"glm-5.1"},{"id":"brand-new-model"}]}`))
	}))
	defer srv.Close()

	svc, _, providerID := newRefreshFixture(t, map[string]string{
		"openai": srv.URL + "/openai",
	}, "")
	got, err := svc.RefreshPreview(context.Background(), OwnerRef{Type: "user", ID: 1}, providerID)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "brand-new-model", got[0].Name)
	require.False(t, got[0].AlreadyInLibrary)
	require.Equal(t, "glm-5.1", got[1].Name)
	require.True(t, got[1].AlreadyInLibrary)
}

// TestRefreshPreviewUnsupported: every configured protocol answers 404/405, so
// the caller is told to enter model names manually.
func TestRefreshPreviewUnsupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	svc, _, providerID := newRefreshFixture(t, map[string]string{
		"openai":    srv.URL + "/openai",
		"anthropic": srv.URL + "/anthropic",
	}, "")
	_, err := svc.RefreshPreview(context.Background(), OwnerRef{Type: "user", ID: 1}, providerID)
	require.ErrorIs(t, err, ErrModelsEndpointUnsupported)
}

// TestRefreshPreview401SurfacesReason: a non-404 failure is reported verbatim
// rather than masked as "unsupported".
func TestRefreshPreview401SurfacesReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad key", http.StatusUnauthorized)
	}))
	defer srv.Close()

	svc, _, providerID := newRefreshFixture(t, map[string]string{
		"openai": srv.URL + "/openai",
	}, "")
	_, err := svc.RefreshPreview(context.Background(), OwnerRef{Type: "user", ID: 1}, providerID)
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrModelsEndpointUnsupported))
	require.Contains(t, err.Error(), "401")
}

// TestRefreshPreviewNoBaseURLs: nothing to probe -> unsupported, no request.
func TestRefreshPreviewNoBaseURLs(t *testing.T) {
	svc, _, providerID := newRefreshFixture(t, map[string]string{}, "")
	_, err := svc.RefreshPreview(context.Background(), OwnerRef{Type: "user", ID: 1}, providerID)
	require.ErrorIs(t, err, ErrModelsEndpointUnsupported)
}

func TestRefreshPreviewProviderNotFound(t *testing.T) {
	svc, _, _ := newRefreshFixture(t, map[string]string{}, "")
	_, err := svc.RefreshPreview(context.Background(), OwnerRef{Type: "user", ID: 1}, 99999)
	require.ErrorIs(t, err, ErrNotFound)
}

// TestRefreshPreviewThenImportDedupes covers the round trip: preview names feed
// ModelLibraryService.Import, which adds only the missing ones. Importing the
// same names again adds nothing and reports every name as skipped.
func TestRefreshPreviewThenImportDedupes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"glm-5.1"},{"id":"new-a"},{"id":"new-b"}]}`))
	}))
	defer srv.Close()

	ctx := context.Background()
	owner := OwnerRef{Type: "user", ID: 1}
	svc, lib, providerID := newRefreshFixture(t, map[string]string{
		"openai": srv.URL + "/openai",
	}, "")
	preview, err := svc.RefreshPreview(ctx, owner, providerID)
	require.NoError(t, err)

	added, skipped, err := lib.Import(ctx, owner, candidateNames(preview))
	require.NoError(t, err)
	require.Equal(t, 2, added)
	require.Equal(t, 1, skipped)

	added, skipped, err = lib.Import(ctx, owner, candidateNames(preview))
	require.NoError(t, err)
	require.Equal(t, 0, added)
	require.Equal(t, 3, skipped)
}

// TestRefreshPreviewRequiresOwner: an invalid owner is rejected before any I/O.
func TestRefreshPreviewRequiresOwner(t *testing.T) {
	svc, _, providerID := newRefreshFixture(t, map[string]string{}, "")
	_, err := svc.RefreshPreview(context.Background(), OwnerRef{}, providerID)
	require.Error(t, err)
}

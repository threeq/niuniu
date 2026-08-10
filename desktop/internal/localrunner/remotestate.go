package localrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// remotestate.go is the HTTP RemoteStateProvider: it reads the workspace diff
// from the bound server (GET /api/workspaces/:id/diff) and reshapes the server's
// []RepoDiff into the []RepoState the GitSyncer applies. It reuses the same
// base URL + bearer token as the reverse channel (#472 "复用既有连接鉴权").

// serverRepoDiff mirrors the fields of server service.RepoDiff we consume. The
// server owns the canonical shape; we decode only what sync needs.
type serverRepoDiff struct {
	Name          string `json:"name"`
	BaseBranch    string `json:"base_branch"`
	CurrentBranch string `json:"current_branch"`
	CloneURL      string `json:"clone_url"`
	Files         []struct {
		RawPatch string `json:"raw_patch"`
	} `json:"files"`
}

// HTTPRemoteState fetches remote git state over REST.
type HTTPRemoteState struct {
	client      *http.Client
	baseURL     string // e.g. https://host:port (no trailing slash)
	workspaceID string
	token       string
}

// NewHTTPRemoteState builds a provider for one bound (server, workspace).
func NewHTTPRemoteState(client *http.Client, baseURL, workspaceID, token string) *HTTPRemoteState {
	if client == nil {
		client = http.DefaultClient
	}
	return &HTTPRemoteState{
		client:      client,
		baseURL:     strings.TrimRight(baseURL, "/"),
		workspaceID: workspaceID,
		token:       token,
	}
}

// Fetch calls GET /api/workspaces/:id/diff and returns one RepoState per repo,
// with the repo's per-file raw patches concatenated into one applyable diff.
func (h *HTTPRemoteState) Fetch(ctx context.Context) ([]RepoState, error) {
	// ?patch=1 asks the server to keep every file's full raw_patch (the default
	// list shape summarises resolved repos and drops patches, which would make
	// sync see no content and apply nothing).
	url := fmt.Sprintf("%s/api/workspaces/%s/diff?patch=1", h.baseURL, h.workspaceID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("workspace diff: unexpected status %d", resp.StatusCode)
	}
	var repos []serverRepoDiff
	if err := json.NewDecoder(resp.Body).Decode(&repos); err != nil {
		return nil, fmt.Errorf("decode workspace diff: %w", err)
	}
	out := make([]RepoState, 0, len(repos))
	for _, r := range repos {
		var patch strings.Builder
		for _, f := range r.Files {
			if f.RawPatch == "" {
				continue
			}
			patch.WriteString(f.RawPatch)
			if !strings.HasSuffix(f.RawPatch, "\n") {
				patch.WriteByte('\n')
			}
		}
		out = append(out, RepoState{
			Name:          r.Name,
			CurrentBranch: r.CurrentBranch,
			BaseBranch:    r.BaseBranch,
			Patch:         patch.String(),
			CloneURL:      r.CloneURL,
		})
	}
	return out, nil
}

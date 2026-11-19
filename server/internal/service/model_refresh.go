package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// ErrModelsEndpointUnsupported means none of the provider's configured
// protocols exposed a usable GET /models endpoint (all answered 404/405 or no
// base_url was configured at all). The UI turns this into "this provider does
// not support model listing — add model names manually".
var ErrModelsEndpointUnsupported = errors.New("provider does not support a /models listing; enter model names manually")

// ModelCandidate is one row of a refresh preview. AlreadyInLibrary tells the
// picker to pre-check/disable names that a previous import already added.
type ModelCandidate struct {
	Name             string `json:"name"`
	AlreadyInLibrary bool   `json:"already_in_library"`
}

// refreshProtocols is the fixed probe order. OpenAI-compatible endpoints are
// the most common; the codex-specific base_url (some platforms expose a
// different Responses endpoint) is tried next; the Anthropic-compatible
// endpoint last because fewer providers list models there.
var refreshProtocols = []string{"openai", "codex", "anthropic"}

// ModelRefreshService previews a provider's model catalogue and feeds the
// shared library. It is read-only towards provider configuration: a refresh
// only discovers names, and an import goes through ModelLibraryService so the
// usual dedupe/source rules apply.
type ModelRefreshService struct {
	q      *store.Queries
	lib    *ModelLibraryService
	client *http.Client
}

func NewModelRefreshService(q *store.Queries, lib *ModelLibraryService) *ModelRefreshService {
	return &ModelRefreshService{
		q:      q,
		lib:    lib,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

// RefreshPreview lists a provider's models without importing anything. It
// resolves the provider's base_url + credential, tries the protocols in
// refreshProtocols order, and stops at the first one that answers. The result
// is deduplicated, sorted, and annotated with whether each name is already in
// the owner's visible library (system rows included).
func (s *ModelRefreshService) RefreshPreview(ctx context.Context, owner OwnerRef, providerID int64) ([]ModelCandidate, error) {
	if err := owner.Validate(); err != nil {
		return nil, err
	}
	provider, err := s.q.GetEnvProvider(ctx, providerID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	accounts, err := s.q.ListEnvAccounts(ctx)
	if err != nil {
		return nil, err
	}
	names, err := s.fetchModelNames(ctx, provider, accounts)
	if err != nil {
		return nil, err
	}

	// One library read for the whole preview: the merged list (disabled rows
	// included) is exactly the "already in library" set.
	library, err := s.lib.List(ctx, owner, true)
	if err != nil {
		return nil, err
	}
	known := make(map[string]struct{}, len(library))
	for _, m := range library {
		known[m.Name] = struct{}{}
	}

	out := make([]ModelCandidate, 0, len(names))
	for _, name := range names {
		_, exists := known[name]
		out = append(out, ModelCandidate{Name: name, AlreadyInLibrary: exists})
	}
	return out, nil
}

// fetchModelNames walks the protocol order and returns the first successful
// listing. A 404/405 means "this protocol has no /models endpoint" and moves
// on; any other failure is remembered so that when every protocol fails the
// caller sees the concrete reason rather than a generic "unsupported".
func (s *ModelRefreshService) fetchModelNames(ctx context.Context, p store.EnvProvider, accounts []store.EnvAccount) ([]string, error) {
	bases := decodeProviderBaseURLs(p.BaseUrls)
	key := resolveProviderKey(p.ApiKey, accounts)

	var lastErr error
	for _, protocol := range refreshProtocols {
		base := strings.TrimSpace(bases[protocol])
		if base == "" {
			continue
		}
		names, err := s.fetchModels(ctx, base, key, protocol)
		if err == nil {
			return names, nil
		}
		if errors.Is(err, errModelsEndpointMissing) {
			continue
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, ErrModelsEndpointUnsupported
}

// errModelsEndpointMissing is the internal "try the next protocol" signal for
// 404/405. It is never returned to callers; they either get a listing or a
// concrete error / ErrModelsEndpointUnsupported.
var errModelsEndpointMissing = errors.New("models endpoint missing")

func (s *ModelRefreshService) fetchModels(ctx context.Context, baseURL, key, protocol string) ([]string, error) {
	endpoint := strings.TrimRight(baseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build models request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if protocol == "anthropic" {
		// Anthropic-compatible endpoints authenticate with x-api-key; providers
		// that only understand Bearer still see one, so send both.
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return nil, errModelsEndpointMissing
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		msg := strings.TrimSpace(string(detail))
		if msg == "" {
			return nil, fmt.Errorf("GET %s: %s", endpoint, resp.Status)
		}
		return nil, fmt.Errorf("GET %s: %s: %s", endpoint, resp.Status, msg)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read models response: %w", err)
	}
	return parseModelNames(body), nil
}

// modelsResponse covers the two documented envelopes: OpenAI-style
// {"data":[...]} and {"models":[...]}. Each entry contributes its id, falling
// back to name.
type modelsResponse struct {
	Data   []modelEntry `json:"data"`
	Models []modelEntry `json:"models"`
}

type modelEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// parseModelNames extracts, trims, deduplicates and sorts every listed name.
func parseModelNames(body []byte) []string {
	var payload modelsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	seen := make(map[string]struct{})
	add := func(entries []modelEntry) {
		for _, e := range entries {
			name := strings.TrimSpace(e.ID)
			if name == "" {
				name = strings.TrimSpace(e.Name)
			}
			if name != "" {
				seen[name] = struct{}{}
			}
		}
	}
	add(payload.Data)
	add(payload.Models)

	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// decodeProviderBaseURLs parses the base_urls JSON (Record<protocol, base_url>).
func decodeProviderBaseURLs(raw string) map[string]string {
	m := map[string]string{}
	if raw == "" {
		return m
	}
	_ = json.Unmarshal([]byte(raw), &m)
	return m
}

const refreshAccountRefPrefix = "${ACCOUNT:"

// resolveProviderKey resolves a "${ACCOUNT:<name>}" api_key against the account
// list, mirroring sceneenv's expansion rule. A literal key passes through; an
// unresolved reference yields "" so no bogus credential is sent.
func resolveProviderKey(raw string, accounts []store.EnvAccount) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.HasPrefix(raw, refreshAccountRefPrefix) || !strings.HasSuffix(raw, "}") {
		return raw
	}
	name := raw[len(refreshAccountRefPrefix) : len(raw)-1]
	if name == "" || strings.Contains(name, "}") {
		return raw
	}
	for _, a := range accounts {
		if a.Name == name {
			return a.ApiKey
		}
	}
	return ""
}

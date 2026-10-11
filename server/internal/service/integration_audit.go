// Integration activity feed — the read side of the two integration audit
// tables (external_api_audit, data_source_audit). Both were write-only before
// Epic #726 / #728: an agent could call an external API or run a data query and
// nothing in the product could answer "which agent called what, and when".
//
// The two tables have different columns, so the feed is merged in Go rather
// than in SQL: each table is queried independently with its own filters and a
// (offset+limit) window, the two windows are merged newest-first, and the
// requested page is sliced out of the result. Fetching offset+limit from each
// side is sufficient — the merged top-N is always contained in the union of
// the two per-table top-Ns — and keeps every query a plain single-table SELECT
// that both SQLite and PostgreSQL can plan.
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// nullTime maps an optional time bound onto the nullable param the audit
// queries use (NULL = unbounded).
func nullTime(t *time.Time) sql.NullTime {
	if t == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *t, Valid: true}
}

// Kinds in the unified feed. They double as the wire values of `kind`.
const (
	ActivityKindAPI  = "api"  // one external_api_audit row
	ActivityKindData = "data" // one data_source_audit row
)

// ErrBadActivityKind is returned for a kind value outside {api, data, ""}.
var ErrBadActivityKind = errors.New("invalid activity kind")

// Page bounds. The cap keeps the read path (and the SPA table) bounded no
// matter what a caller asks for; the default matches the settings page size.
const (
	defaultActivityLimit = 50
	maxActivityLimit     = 200
)

// IntegrationActivityInput is the filter set for the feed. All filters are
// optional; the zero value returns the caller's newest activity of both kinds.
//
// Kind selects which audit table(s) to read ("" = both).
//
// ProviderID / SourceID narrow the feed to ONE integration: since a provider
// only ever produces API rows and a data source only ever produces data rows,
// setting either also narrows Kind to that side. That is what makes a single
// "source" picker in the settings UI behave the way a user expects.
type IntegrationActivityInput struct {
	UserID     int64
	Kind       string // "", ActivityKindAPI, ActivityKindData
	ProviderID int64  // > 0 narrows to API calls of that provider
	SourceID   int64  // > 0 narrows to data queries of that source
	From       time.Time
	To         time.Time
	Limit      int64
	Offset     int64
}

// IntegrationActivityItem is one row of the merged feed. The two kinds share a
// envelope (id/kind/user_id/created_at) and carry their own fields; the
// kind-specific ones are omitted when they do not apply, so the JSON stays
// readable and the SPA can switch on `kind`.
type IntegrationActivityItem struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	UserID    int64     `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`

	// kind == "api"
	ProviderID   *int64   `json:"provider_id,omitempty"`
	ProviderName string   `json:"provider_name,omitempty"`
	Method       string   `json:"method,omitempty"`
	Path         string   `json:"path,omitempty"`
	StatusCode   *int64   `json:"status_code,omitempty"`
	BodySHA256   string   `json:"body_sha256,omitempty"`
	BodyBytes    int64    `json:"body_bytes,omitempty"`
	BodyFields   []string `json:"body_fields,omitempty"`

	// kind == "data"
	SourceID         *int64   `json:"source_id,omitempty"`
	SourceName       string   `json:"source_name,omitempty"`
	AccessMode       string   `json:"access_mode,omitempty"`
	DatabaseName     string   `json:"database_name,omitempty"`
	Objects          []string `json:"objects,omitempty"`
	StatementSummary string   `json:"statement_summary,omitempty"`
	Status           string   `json:"status,omitempty"`
	Rows             *int64   `json:"rows,omitempty"`
	DurationMs       *int64   `json:"duration_ms,omitempty"`
}

// IntegrationActivityPage is the wire shape of one page of the feed. Total is
// the number of rows matching the filters across BOTH tables (before paging),
// so the SPA can render "showing X of Y".
type IntegrationActivityPage struct {
	Items  []IntegrationActivityItem `json:"items"`
	Total  int64                     `json:"total"`
	Limit  int64                     `json:"limit"`
	Offset int64                     `json:"offset"`
}

// IntegrationAuditService reads the two integration audit tables.
type IntegrationAuditService struct {
	q *store.Queries
}

// NewIntegrationAuditService wires the reader.
func NewIntegrationAuditService(q *store.Queries) *IntegrationAuditService {
	return &IntegrationAuditService{q: q}
}

// ListActivity returns one page of the caller's merged integration activity.
//
// The feed is scoped to in.UserID: audit rows are written with the calling
// user's id, so this is "my calls", never another user's. A cross-user /
// org-wide view is deliberately NOT built here — that is the team-edition
// concern tracked separately (#743).
func (s *IntegrationAuditService) ListActivity(ctx context.Context, in IntegrationActivityInput) (*IntegrationActivityPage, error) {
	if in.Kind != "" && in.Kind != ActivityKindAPI && in.Kind != ActivityKindData {
		return nil, ErrBadActivityKind
	}

	limit := in.Limit
	if limit <= 0 {
		limit = defaultActivityLimit
	}
	if limit > maxActivityLimit {
		limit = maxActivityLimit
	}
	offset := in.Offset
	if offset < 0 {
		offset = 0
	}

	// Which tables this request reads. Kind picks the sides; an id filter can
	// only ever match rows on its own side (a provider emits no data rows, a
	// data source emits no api rows), so it can only ever remove a side — never
	// force one back on over an explicit Kind. That keeps kind=data plus a
	// provider filter empty rather than silently returning api rows.
	wantAPI := in.Kind == "" || in.Kind == ActivityKindAPI
	wantData := in.Kind == "" || in.Kind == ActivityKindData
	if in.ProviderID > 0 {
		wantData = false
	}
	if in.SourceID > 0 {
		wantAPI = false
	}

	// The merged page [offset, offset+limit) is always contained in the union
	// of the two per-table prefixes of that length, so that is all we fetch.
	window := offset + limit
	var fromTS, toTS *time.Time
	if !in.From.IsZero() {
		t := in.From
		fromTS = &t
	}
	if !in.To.IsZero() {
		t := in.To
		toTS = &t
	}

	items := []IntegrationActivityItem{}
	var total int64

	if wantAPI {
		rows, err := s.q.ListExternalApiAudit(ctx, store.ListExternalApiAuditParams{
			UserID:     in.UserID,
			ProviderID: in.ProviderID,
			FromTs:     nullTime(fromTS),
			ToTs:       nullTime(toTS),
			Off:        0,
			Lim:        window,
		})
		if err != nil {
			return nil, err
		}
		n, err := s.q.CountExternalApiAudit(ctx, store.CountExternalApiAuditParams{
			UserID:     in.UserID,
			ProviderID: in.ProviderID,
			FromTs:     nullTime(fromTS),
			ToTs:       nullTime(toTS),
		})
		if err != nil {
			return nil, err
		}
		total += n
		for _, r := range rows {
			items = append(items, apiActivityItem(r))
		}
	}

	if wantData {
		rows, err := s.q.ListDataSourceAudit(ctx, store.ListDataSourceAuditParams{
			UserID:   in.UserID,
			SourceID: in.SourceID,
			FromTs:   nullTime(fromTS),
			ToTs:     nullTime(toTS),
			Off:      0,
			Lim:      window,
		})
		if err != nil {
			return nil, err
		}
		n, err := s.q.CountDataSourceAudit(ctx, store.CountDataSourceAuditParams{
			UserID:   in.UserID,
			SourceID: in.SourceID,
			FromTs:   nullTime(fromTS),
			ToTs:     nullTime(toTS),
		})
		if err != nil {
			return nil, err
		}
		total += n
		for _, r := range rows {
			items = append(items, dataActivityItem(r))
		}
	}

	// Newest first. created_at is the display key; id breaks ties so a page
	// boundary never drops or duplicates a same-second pair.
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.After(items[j].CreatedAt)
		}
		return items[i].ID > items[j].ID
	})

	page := sliceActivityPage(items, offset, limit)
	s.enrichNames(ctx, page)
	return &IntegrationActivityPage{Items: page, Total: total, Limit: limit, Offset: offset}, nil
}

// sliceActivityPage returns items[offset:offset+limit] as a non-nil slice.
func sliceActivityPage(items []IntegrationActivityItem, offset, limit int64) []IntegrationActivityItem {
	if offset >= int64(len(items)) {
		return []IntegrationActivityItem{}
	}
	end := offset + limit
	if end > int64(len(items)) {
		end = int64(len(items))
	}
	return items[offset:end]
}

// enrichNames fills provider_name / source_name for the page's rows. Names are
// resolved per page (not per row) and only for ids actually present. A lookup
// failure is not fatal — the feed still renders with ids, and the SPA keeps its
// own name maps anyway.
func (s *IntegrationAuditService) enrichNames(ctx context.Context, items []IntegrationActivityItem) {
	var providerIDs, sourceIDs []int64
	seenP, seenS := map[int64]bool{}, map[int64]bool{}
	for _, it := range items {
		if it.ProviderID != nil && !seenP[*it.ProviderID] {
			seenP[*it.ProviderID] = true
			providerIDs = append(providerIDs, *it.ProviderID)
		}
		if it.SourceID != nil && !seenS[*it.SourceID] {
			seenS[*it.SourceID] = true
			sourceIDs = append(sourceIDs, *it.SourceID)
		}
	}
	if len(providerIDs) == 0 && len(sourceIDs) == 0 {
		return
	}

	providerNames := map[int64]string{}
	if len(providerIDs) > 0 {
		if provs, err := s.q.ListProviders(ctx); err == nil {
			want := map[int64]bool{}
			for _, id := range providerIDs {
				want[id] = true
			}
			for _, p := range provs {
				if want[p.ID] {
					providerNames[p.ID] = p.Name
				}
			}
		}
	}
	sourceNames := map[int64]string{}
	if len(sourceIDs) > 0 {
		if rows, err := s.q.ListDataSourceNamesByIDs(ctx, sourceIDs); err == nil {
			for _, r := range rows {
				sourceNames[r.ID] = r.Name
			}
		}
	}

	for i := range items {
		if items[i].ProviderID != nil {
			items[i].ProviderName = providerNames[*items[i].ProviderID]
		}
		if items[i].SourceID != nil {
			items[i].SourceName = sourceNames[*items[i].SourceID]
		}
	}
}

func apiActivityItem(r store.ExternalApiAudit) IntegrationActivityItem {
	pid, code := r.ProviderID, r.StatusCode
	it := IntegrationActivityItem{
		ID:         r.ID,
		Kind:       ActivityKindAPI,
		UserID:     r.UserID,
		CreatedAt:  r.CreatedAt,
		ProviderID: &pid,
		Method:     r.Method,
		Path:       r.Path,
		StatusCode: &code,
		BodySHA256: r.BodySha256,
		BodyBytes:  r.BodyBytes,
	}
	// body_fields is stored as a JSON array; a bodyless read stores "[]", which
	// decodes to an empty slice and drops out via omitempty.
	var fields []string
	if err := json.Unmarshal([]byte(r.BodyFields), &fields); err == nil && len(fields) > 0 {
		it.BodyFields = fields
	}
	return it
}

func dataActivityItem(r store.DataSourceAudit) IntegrationActivityItem {
	sid, n, ms := r.SourceID, r.Rows, r.DurationMs
	it := IntegrationActivityItem{
		ID:               r.ID,
		Kind:             ActivityKindData,
		UserID:           r.UserID,
		CreatedAt:        r.CreatedAt,
		SourceID:         &sid,
		AccessMode:       r.AccessMode,
		DatabaseName:     r.DatabaseName,
		StatementSummary: r.StatementSummary,
		Status:           r.Status,
		Rows:             &n,
		DurationMs:       &ms,
	}
	var objs []string
	if err := json.Unmarshal([]byte(r.Objects), &objs); err == nil && len(objs) > 0 {
		it.Objects = objs
	}
	return it
}

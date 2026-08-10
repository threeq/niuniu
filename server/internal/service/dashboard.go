// Package service — DashboardService manages the data-dashboard subsystem
// (spec docs/superpowers/specs/2026-06-04-data-integration-and-dashboard-design.md
// section 7): owner-scoped saved (read-only) queries, dashboards, and the panels
// that bind a saved query to a dashboard.
//
// Three invariants enforced here:
//
//  1. Saved queries are READ-ONLY. SaveQuery (and Pin) classify the statement
//     through the connector for the source's kind; a write statement is rejected
//     with ErrSavedQueryNotReadOnly (handlers map it to 422). The dashboard only
//     ever runs reads, so a panel can never mutate a database.
//  2. owner filtering. Every read/write takes (userID, orgIDs); a row is visible
//     when it is the caller's personal row or an org row the caller belongs to.
//     Cross-tenant ids come back as ErrDashboardNotFound / ErrSavedQueryNotFound.
//  3. PanelData reuses the data-proxy gate. It never executes SQL directly: it
//     loads the panel + its saved query (one JOIN read), then calls
//     DataProxyService.Query, so read/write separation, data-scope authorization,
//     per-op confirmation, and audit all apply exactly as they do for a live
//     agent query.
//
// Pin is the "click chart -> pin to dashboard" landing point: it saves the
// read-only query (recording the origin workspace), creates the owner's default
// dashboard on first pin (EnsureDefaultDashboard), and adds the panel. Creating
// the first dashboard is what makes the "data dashboard" nav entry appear in the
// SPA (CountDashboards turns positive).
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/dataconn"
	"github.com/niuniu-dev/niuniu/internal/store"
)

var (
	// ErrSavedQueryNotFound is returned when no saved_query matches the id within
	// the caller's accessible owner set (not-found and authz miss share an error
	// so callers cannot probe existence of rows they cannot see).
	ErrSavedQueryNotFound = errors.New("saved query not found")
	// ErrDashboardNotFound mirrors ErrSavedQueryNotFound for dashboards/panels.
	ErrDashboardNotFound = errors.New("dashboard not found")
	// ErrPanelNotFound is returned when a panel id is missing or out of scope.
	ErrPanelNotFound = errors.New("dashboard panel not found")
)

// ErrSavedQueryNotReadOnly wraps the classify result for a write statement so
// handlers can map it to 422 while callers can still errors.Is against it.
type ErrSavedQueryNotReadOnly struct{ Reason string }

func (e *ErrSavedQueryNotReadOnly) Error() string {
	if e.Reason == "" {
		return "saved query must be a read-only statement"
	}
	return "saved query must be a read-only statement: " + e.Reason
}

// defaultDashboardName is the name given to the auto-created dashboard on the
// first pin. The SPA renders the same i18n string for the nav entry / page
// title; this server-side default is the fallback used when no name is supplied.
const defaultDashboardName = "我的数据看板"

// dataProxyQuerier is the seam DashboardService uses to run panel data through
// the three-layer gate. *DataProxyService satisfies it; tests inject a fake.
type dataProxyQuerier interface {
	Query(ctx context.Context, in DataQueryInput) (*dataconn.ResultSet, error)
}

// DashboardService coordinates saved queries, dashboards, and panels. It holds
// the sqlc queries, the data-source service (source load + classify kind), the
// connector registry (Classify for the read-only gate), and the data proxy
// (PanelData execution).
type DashboardService struct {
	q        *store.Queries
	sources  *DataSourceService
	registry *dataconn.Registry
	proxy    dataProxyQuerier
}

// NewDashboardService wires the dashboard service. proxy may be a
// *DataProxyService in production; tests pass a fake querier via
// NewDashboardServiceWithSeams.
func NewDashboardService(q *store.Queries, sources *DataSourceService, reg *dataconn.Registry, proxy *DataProxyService) *DashboardService {
	var p dataProxyQuerier
	if proxy != nil { // avoid a typed-nil satisfying the interface
		p = proxy
	}
	return &DashboardService{q: q, sources: sources, registry: reg, proxy: p}
}

// NewDashboardServiceWithSeams is the test-friendly constructor accepting the
// proxy seam directly so tests can inject a fake querier.
func NewDashboardServiceWithSeams(q *store.Queries, sources *DataSourceService, reg *dataconn.Registry, proxy dataProxyQuerier) *DashboardService {
	return &DashboardService{q: q, sources: sources, registry: reg, proxy: proxy}
}

// --- DTOs ---

// SavedQueryDTO is the API representation of a saved query. WorkspaceID is the
// nullable origin workspace (NULL -> nil) used for click-to-jump.
type SavedQueryDTO struct {
	ID          int64          `json:"id"`
	OwnerType   string         `json:"owner_type"`
	OwnerID     int64          `json:"owner_id"`
	SourceID    int64          `json:"source_id"`
	WorkspaceID *int64         `json:"workspace_id"`
	Name        string         `json:"name"`
	Operation   map[string]any `json:"operation"`
	ChartSpec   map[string]any `json:"chart_spec"`
}

// DashboardDTO is the API representation of a dashboard.
type DashboardDTO struct {
	ID        int64          `json:"id"`
	OwnerType string         `json:"owner_type"`
	OwnerID   int64          `json:"owner_id"`
	Name      string         `json:"name"`
	Layout    map[string]any `json:"layout"`
}

// PanelDTO is the API representation of a dashboard panel. WorkspaceID is the
// origin workspace of the panel's saved query (nullable) so the SPA can render
// the panel as a link back to the workspace it was pinned from.
type PanelDTO struct {
	ID                 int64          `json:"id"`
	DashboardID        int64          `json:"dashboard_id"`
	SavedQueryID       int64          `json:"saved_query_id"`
	Title              string         `json:"title"`
	VizType            string         `json:"viz_type"`
	ChartSpec          map[string]any `json:"chart_spec"`
	GridX              int64          `json:"grid_x"`
	GridY              int64          `json:"grid_y"`
	GridW              int64          `json:"grid_w"`
	GridH              int64          `json:"grid_h"`
	RefreshIntervalSec int64          `json:"refresh_interval_sec"`
	WorkspaceID        *int64         `json:"workspace_id"`
	SourceID           int64          `json:"source_id"`
}

// --- Saved queries ---

// SaveQueryInput is what handlers pass to SaveQuery / Pin. WorkspaceID is the
// origin workspace (0 -> stored as NULL). Operation must be a read-only
// statement; it is classified before insert.
type SaveQueryInput struct {
	OwnerType string
	OwnerID   int64
	UserID    int64
	OrgIDs    []int64
	// SourceID is the backing data source. 0 means "no source / static": the
	// saved query is a self-contained snapshot (e.g. a niuniu-data block with
	// chart.type='echarts'), not a re-runnable DB query.
	SourceID    int64
	WorkspaceID int64
	Name        string
	Operation   map[string]any
	ChartSpec   map[string]any
	// Snapshot is the inline payload for a static (source-less) saved query,
	// e.g. {"result": <ResultSet or null>}. Ignored when SourceID > 0.
	Snapshot map[string]any
}

// SaveQuery validates that the statement is read-only (classify via the source's
// connector) and persists the saved query. Returns *ErrSavedQueryNotReadOnly
// (422) for a write statement, ErrDataSourceNotFound when the source is out of
// scope, or a plain error on a bad/unrecognized statement.
func (s *DashboardService) SaveQuery(ctx context.Context, in SaveQueryInput) (int64, error) {
	if err := validateOwnerType(in.OwnerType); err != nil {
		return 0, err
	}
	if strings.TrimSpace(in.Name) == "" {
		return 0, fmt.Errorf("saved query name is required")
	}
	// A static (source-less) saved query stores its chart + an inline snapshot
	// and is NOT re-runnable, so the read-only gate (which requires a source +
	// statement) is skipped. A query-backed saved query (SourceID > 0) must pass
	// the gate: classify through the source's connector, which also validates the
	// source is accessible to the caller (owner-filtered Get).
	static := in.SourceID <= 0
	if !static {
		if err := s.assertReadOnly(ctx, in.SourceID, in.UserID, in.OrgIDs, in.Operation); err != nil {
			return 0, err
		}
	}
	opJSON, err := marshalJSONObject(in.Operation)
	if err != nil {
		return 0, fmt.Errorf("marshal operation: %w", err)
	}
	chartJSON, err := marshalJSONObject(in.ChartSpec)
	if err != nil {
		return 0, fmt.Errorf("marshal chart_spec: %w", err)
	}
	snapshot := ""
	if static {
		// Record when the snapshot's data was obtained so the panel can show a
		// "data time". Prefer the time the chat query stamped onto the result;
		// otherwise stamp the pin time as the capture time.
		in.Snapshot = ensureSnapshotQueriedAt(in.Snapshot)
		snapJSON, serr := marshalJSONObject(in.Snapshot)
		if serr != nil {
			return 0, fmt.Errorf("marshal snapshot: %w", serr)
		}
		snapshot = snapJSON
	}
	id, err := s.q.CreateSavedQuery(ctx, store.CreateSavedQueryParams{
		OwnerType:   in.OwnerType,
		OwnerID:     in.OwnerID,
		SourceID:    nullInt64FromID(in.SourceID),
		WorkspaceID: nullInt64FromID(in.WorkspaceID),
		Name:        strings.TrimSpace(in.Name),
		Operation:   opJSON,
		ChartSpec:   chartJSON,
		Snapshot:    snapshot,
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// assertReadOnly loads the source (owner-filtered), builds the connector
// Operation for the source's kind family from the saved operation map (SQL
// statement OR the structured redis/mongo/elasticsearch/http fields), classifies
// it, and rejects writes. A live panel re-runs this exact operation, so every
// supported source kind — not just SQL — can back a re-runnable saved query.
func (s *DashboardService) assertReadOnly(ctx context.Context, sourceID, userID int64, orgIDs []int64, op map[string]any) error {
	src, err := s.sources.Get(ctx, sourceID, userID, orgIDs)
	if err != nil {
		return err
	}
	kind := dataconn.SourceKind(src.Kind)
	conn, err := s.registry.Get(kind)
	if err != nil {
		return &ErrDataSourceKindUnsupported{Kind: src.Kind}
	}
	// Reuse the data-proxy's per-kind shape validation + Operation construction
	// (statement XOR the kind's structured fields). Database/limits are not
	// needed for read/write classification, so an empty ConnConfig is fine.
	operation, err := buildOperation(kind, dataQueryInputFromOp(op), dataconn.ConnConfig{})
	if err != nil {
		return &ErrSavedQueryNotReadOnly{Reason: err.Error()}
	}
	mode, _, err := conn.Classify(operation)
	if err != nil {
		return &ErrSavedQueryNotReadOnly{Reason: err.Error()}
	}
	if mode != dataconn.ModeRead {
		return &ErrSavedQueryNotReadOnly{Reason: "write operations are not allowed on a saved query"}
	}
	return nil
}

// ListSavedQueries returns the caller's accessible saved queries.
func (s *DashboardService) ListSavedQueries(ctx context.Context, userID int64, orgIDs []int64) ([]SavedQueryDTO, error) {
	rows, err := s.q.ListSavedQueriesForOwners(ctx, store.ListSavedQueriesForOwnersParams{
		OwnerID: userID,
		OrgIds:  orgIDs,
	})
	if err != nil {
		return nil, err
	}
	out := make([]SavedQueryDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, savedQueryRowToDTO(r))
	}
	return out, nil
}

// GetSavedQuery returns a single saved query if accessible, else ErrSavedQueryNotFound.
func (s *DashboardService) GetSavedQuery(ctx context.Context, id, userID int64, orgIDs []int64) (*SavedQueryDTO, error) {
	row, err := s.getOwnedSavedQuery(ctx, id, userID, orgIDs)
	if err != nil {
		return nil, err
	}
	dto := savedQueryRowToDTO(row)
	return &dto, nil
}

// DeleteSavedQuery removes a saved query the caller can access.
func (s *DashboardService) DeleteSavedQuery(ctx context.Context, id, userID int64, orgIDs []int64) error {
	if _, err := s.getOwnedSavedQuery(ctx, id, userID, orgIDs); err != nil {
		return err
	}
	return s.q.DeleteSavedQuery(ctx, id)
}

func (s *DashboardService) getOwnedSavedQuery(ctx context.Context, id, userID int64, orgIDs []int64) (store.SavedQuery, error) {
	row, err := s.q.GetSavedQuery(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return store.SavedQuery{}, ErrSavedQueryNotFound
	}
	if err != nil {
		return store.SavedQuery{}, err
	}
	if !ownerVisible(row.OwnerType, row.OwnerID, userID, orgIDs) {
		return store.SavedQuery{}, ErrSavedQueryNotFound
	}
	return row, nil
}

// --- Dashboards ---

// CreateDashboardInput is what handlers pass to CreateDashboard.
type CreateDashboardInput struct {
	OwnerType string
	OwnerID   int64
	Name      string
	Layout    map[string]any
}

// CreateDashboard inserts a new dashboard for the given owner.
func (s *DashboardService) CreateDashboard(ctx context.Context, in CreateDashboardInput) (int64, error) {
	if err := validateOwnerType(in.OwnerType); err != nil {
		return 0, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = defaultDashboardName
	}
	layoutJSON, err := marshalJSONObject(in.Layout)
	if err != nil {
		return 0, fmt.Errorf("marshal layout: %w", err)
	}
	return s.q.CreateDashboard(ctx, store.CreateDashboardParams{
		OwnerType: in.OwnerType,
		OwnerID:   in.OwnerID,
		Name:      name,
		Layout:    layoutJSON,
	})
}

// ListDashboards returns the caller's accessible dashboards.
func (s *DashboardService) ListDashboards(ctx context.Context, userID int64, orgIDs []int64) ([]DashboardDTO, error) {
	rows, err := s.q.ListDashboardsForOwners(ctx, store.ListDashboardsForOwnersParams{
		OwnerID: userID,
		OrgIds:  orgIDs,
	})
	if err != nil {
		return nil, err
	}
	out := make([]DashboardDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, dashboardRowToDTO(r))
	}
	return out, nil
}

// CountDashboards returns how many dashboards the caller can access. The SPA
// gates the "data dashboard" nav entry on this being > 0 (entry appears after
// the first pin).
func (s *DashboardService) CountDashboards(ctx context.Context, userID int64, orgIDs []int64) (int64, error) {
	return s.q.CountDashboardsForOwners(ctx, store.CountDashboardsForOwnersParams{
		OwnerID: userID,
		OrgIds:  orgIDs,
	})
}

// GetDashboard returns a single dashboard if accessible.
func (s *DashboardService) GetDashboard(ctx context.Context, id, userID int64, orgIDs []int64) (*DashboardDTO, error) {
	row, err := s.getOwnedDashboard(ctx, id, userID, orgIDs)
	if err != nil {
		return nil, err
	}
	dto := dashboardRowToDTO(row)
	return &dto, nil
}

// UpdateDashboard renames / re-layouts a dashboard the caller can access.
func (s *DashboardService) UpdateDashboard(ctx context.Context, id, userID int64, orgIDs []int64, name string, layout map[string]any) error {
	if _, err := s.getOwnedDashboard(ctx, id, userID, orgIDs); err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("dashboard name is required")
	}
	layoutJSON, err := marshalJSONObject(layout)
	if err != nil {
		return fmt.Errorf("marshal layout: %w", err)
	}
	return s.q.UpdateDashboard(ctx, store.UpdateDashboardParams{
		Name:   strings.TrimSpace(name),
		Layout: layoutJSON,
		ID:     id,
	})
}

// DeleteDashboard removes a dashboard the caller can access (panels cascade via FK).
func (s *DashboardService) DeleteDashboard(ctx context.Context, id, userID int64, orgIDs []int64) error {
	if _, err := s.getOwnedDashboard(ctx, id, userID, orgIDs); err != nil {
		return err
	}
	return s.q.DeleteDashboard(ctx, id)
}

func (s *DashboardService) getOwnedDashboard(ctx context.Context, id, userID int64, orgIDs []int64) (store.Dashboard, error) {
	row, err := s.q.GetDashboard(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Dashboard{}, ErrDashboardNotFound
	}
	if err != nil {
		return store.Dashboard{}, err
	}
	if !ownerVisible(row.OwnerType, row.OwnerID, userID, orgIDs) {
		return store.Dashboard{}, ErrDashboardNotFound
	}
	return row, nil
}

// EnsureDefaultDashboard returns the id of an existing accessible dashboard for
// the owner, or creates one named defaultDashboardName when none exists. Used by
// Pin so the first pin materializes the nav entry.
func (s *DashboardService) EnsureDefaultDashboard(ctx context.Context, ownerType string, ownerID, userID int64, orgIDs []int64) (int64, error) {
	rows, err := s.q.ListDashboardsForOwners(ctx, store.ListDashboardsForOwnersParams{
		OwnerID: userID,
		OrgIds:  orgIDs,
	})
	if err != nil {
		return 0, err
	}
	// Prefer a dashboard owned by the exact target owner; fall back to any
	// accessible one (personal pins land in the personal default).
	for _, r := range rows {
		if r.OwnerType == ownerType && r.OwnerID == ownerID {
			return r.ID, nil
		}
	}
	return s.CreateDashboard(ctx, CreateDashboardInput{
		OwnerType: ownerType,
		OwnerID:   ownerID,
		Name:      defaultDashboardName,
	})
}

// --- Panels ---

// AddPanelInput binds a saved query to a dashboard as a panel.
type AddPanelInput struct {
	DashboardID        int64
	SavedQueryID       int64
	Title              string
	VizType            string
	ChartSpec          map[string]any
	GridX              int64
	GridY              int64
	GridW              int64
	GridH              int64
	RefreshIntervalSec int64
}

// AddPanel adds a panel to a dashboard the caller can access. The saved query
// must also be accessible to the caller (cross-tenant binding is rejected).
func (s *DashboardService) AddPanel(ctx context.Context, in AddPanelInput, userID int64, orgIDs []int64) (int64, error) {
	if _, err := s.getOwnedDashboard(ctx, in.DashboardID, userID, orgIDs); err != nil {
		return 0, err
	}
	if _, err := s.getOwnedSavedQuery(ctx, in.SavedQueryID, userID, orgIDs); err != nil {
		return 0, err
	}
	vizType := strings.TrimSpace(in.VizType)
	if vizType == "" {
		vizType = "table"
	}
	gridW := in.GridW
	if gridW <= 0 {
		gridW = 6
	}
	gridH := in.GridH
	if gridH <= 0 {
		gridH = 4
	}
	chartJSON, err := marshalJSONObject(in.ChartSpec)
	if err != nil {
		return 0, fmt.Errorf("marshal chart_spec: %w", err)
	}
	return s.q.CreatePanel(ctx, store.CreatePanelParams{
		DashboardID:        in.DashboardID,
		SavedQueryID:       in.SavedQueryID,
		Title:              in.Title,
		VizType:            vizType,
		ChartSpec:          chartJSON,
		GridX:              in.GridX,
		GridY:              in.GridY,
		GridW:              gridW,
		GridH:              gridH,
		RefreshIntervalSec: in.RefreshIntervalSec,
	})
}

// ListPanels returns the panels of a dashboard the caller can access, each with
// its origin workspace + source id (from the joined saved query).
func (s *DashboardService) ListPanels(ctx context.Context, dashboardID, userID int64, orgIDs []int64) ([]PanelDTO, error) {
	if _, err := s.getOwnedDashboard(ctx, dashboardID, userID, orgIDs); err != nil {
		return nil, err
	}
	rows, err := s.q.ListPanelsWithQueryByDashboard(ctx, dashboardID)
	if err != nil {
		return nil, err
	}
	out := make([]PanelDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, listPanelRowToDTO(r))
	}
	return out, nil
}

// DeletePanel removes a panel from a dashboard the caller can access.
func (s *DashboardService) DeletePanel(ctx context.Context, dashboardID, panelID, userID int64, orgIDs []int64) error {
	if _, err := s.getOwnedDashboard(ctx, dashboardID, userID, orgIDs); err != nil {
		return err
	}
	panel, err := s.q.GetPanel(ctx, panelID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPanelNotFound
	}
	if err != nil {
		return err
	}
	if panel.DashboardID != dashboardID {
		return ErrPanelNotFound
	}
	return s.q.DeletePanel(ctx, panelID)
}

// MovePanel moves a panel from srcDashboardID to targetDashboardID (both
// accessible to the caller). ErrPanelNotFound if the panel isn't on the source
// dashboard; ErrDashboardNotFound if either dashboard is out of scope. Moving to
// the same dashboard is a no-op.
func (s *DashboardService) MovePanel(ctx context.Context, srcDashboardID, panelID, targetDashboardID, userID int64, orgIDs []int64) error {
	if _, err := s.getOwnedDashboard(ctx, srcDashboardID, userID, orgIDs); err != nil {
		return err
	}
	if _, err := s.getOwnedDashboard(ctx, targetDashboardID, userID, orgIDs); err != nil {
		return err
	}
	panel, err := s.q.GetPanel(ctx, panelID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPanelNotFound
	}
	if err != nil {
		return err
	}
	if panel.DashboardID != srcDashboardID {
		return ErrPanelNotFound
	}
	if targetDashboardID == srcDashboardID {
		return nil
	}
	return s.q.MovePanelToDashboard(ctx, store.MovePanelToDashboardParams{
		DashboardID: targetDashboardID,
		ID:          panelID,
	})
}

// CopyPanel duplicates a panel onto targetDashboardID (both accessible). The
// copy references the SAME saved query, so a live panel stays live and a static
// one keeps its snapshot — it renders/re-runs identically. Returns the new id.
func (s *DashboardService) CopyPanel(ctx context.Context, srcDashboardID, panelID, targetDashboardID, userID int64, orgIDs []int64) (int64, error) {
	if _, err := s.getOwnedDashboard(ctx, srcDashboardID, userID, orgIDs); err != nil {
		return 0, err
	}
	if _, err := s.getOwnedDashboard(ctx, targetDashboardID, userID, orgIDs); err != nil {
		return 0, err
	}
	panel, err := s.q.GetPanel(ctx, panelID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrPanelNotFound
	}
	if err != nil {
		return 0, err
	}
	if panel.DashboardID != srcDashboardID {
		return 0, ErrPanelNotFound
	}
	return s.q.CreatePanel(ctx, store.CreatePanelParams{
		DashboardID:        targetDashboardID,
		SavedQueryID:       panel.SavedQueryID,
		Title:              panel.Title,
		VizType:            panel.VizType,
		ChartSpec:          panel.ChartSpec,
		GridX:              panel.GridX,
		GridY:              panel.GridY,
		GridW:              panel.GridW,
		GridH:              panel.GridH,
		RefreshIntervalSec: panel.RefreshIntervalSec,
	})
}

// WorkspacePanelDTO is a panel pinned from a workspace, plus the name of the
// dashboard it lives on (for the in-workspace data view).
type WorkspacePanelDTO struct {
	PanelDTO
	DashboardName string `json:"dashboard_name"`
}

// ListPanelsForWorkspace returns the panels pinned from a workspace across the
// caller's accessible dashboards (newest first). Backs the in-workspace "data"
// drawer; empty when the workspace has pinned nothing.
func (s *DashboardService) ListPanelsForWorkspace(ctx context.Context, workspaceID, userID int64, orgIDs []int64) ([]WorkspacePanelDTO, error) {
	rows, err := s.q.ListPanelsForWorkspace(ctx, store.ListPanelsForWorkspaceParams{
		WorkspaceID: sql.NullInt64{Int64: workspaceID, Valid: true},
		OwnerID:     userID,
		OrgIds:      orgIDs,
	})
	if err != nil {
		return nil, err
	}
	out := make([]WorkspacePanelDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, WorkspacePanelDTO{
			PanelDTO: PanelDTO{
				ID:                 r.ID,
				DashboardID:        r.DashboardID,
				SavedQueryID:       r.SavedQueryID,
				Title:              r.Title,
				VizType:            r.VizType,
				ChartSpec:          parseJSONObject(r.ChartSpec),
				GridX:              r.GridX,
				GridY:              r.GridY,
				GridW:              r.GridW,
				GridH:              r.GridH,
				RefreshIntervalSec: r.RefreshIntervalSec,
				WorkspaceID:        nullInt64ToPtr(r.SqWorkspaceID),
				SourceID:           r.SqSourceID.Int64,
			},
			DashboardName: r.DashboardName,
		})
	}
	return out, nil
}

// PanelData loads a panel + its saved query (one JOIN read, scoped to the
// caller's dashboard) and runs the read-only query through DataProxyService so
// the three-layer gate + audit apply. Returns *dataconn.ResultSet.
func (s *DashboardService) PanelData(ctx context.Context, dashboardID, panelID, userID int64, orgIDs []int64) (*dataconn.ResultSet, error) {
	// Dashboard access gate first so a cross-tenant dashboard id can't be probed.
	if _, err := s.getOwnedDashboard(ctx, dashboardID, userID, orgIDs); err != nil {
		return nil, err
	}
	row, err := s.q.GetPanelWithQuery(ctx, panelID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPanelNotFound
	}
	if err != nil {
		return nil, err
	}
	if row.DashboardID != dashboardID {
		return nil, ErrPanelNotFound
	}
	// Static (source-less) panel: no re-runnable query. Render from the stored
	// snapshot without touching the data proxy. The chart spec (echarts option)
	// is rendered client-side from panel.chart_spec; the snapshot only carries
	// an optional ResultSet for table/derived charts.
	if !row.SqSourceID.Valid {
		return staticPanelResult(row.SqSnapshot), nil
	}
	if s.proxy == nil {
		return nil, fmt.Errorf("data proxy not configured")
	}
	// Re-run the saved operation, whatever its kind: a SQL statement OR the
	// structured redis/mongo/elasticsearch/http operation. Every supported source
	// can back a live panel (the data proxy's buildOperation validates the shape).
	op := parseJSONObject(row.SqOperation)
	in := dataQueryInputFromOp(op)
	// The data proxy re-loads the source owner-filtered and re-runs the full gate
	// (read/write separation, scope, confirm). Saved queries are read-only by
	// construction, so this is a read path; the proxy still audits it.
	//
	// Attribute the op to the saved query's REAL owner (which may be an org), not
	// the calling user, so audit + any confirmation card carry the correct owner.
	// The source load still uses (userID, orgIDs): the proxy needs the caller's
	// accessible set to decrypt/authorize the source, and an org member can read
	// an org-owned saved query against an org-owned source.
	in.UserID = userID
	in.OrgIDs = orgIDs
	in.OwnerType = row.SqOwnerType
	in.OwnerID = row.SqOwnerID
	in.SourceID = row.SqSourceID.Int64
	// If the saved query carries an origin workspace, thread it through so any
	// confirmation card / audit attributes to that workspace.
	if row.SqWorkspaceID.Valid {
		in.WorkspaceID = row.SqWorkspaceID.Int64
	}
	return s.proxy.Query(ctx, in)
}

// --- Pin ---

// PinInput is the one-step "pin chart to dashboard" request. DashboardID == 0
// routes to EnsureDefaultDashboard (auto-create on first pin).
type PinInput struct {
	OwnerType string
	OwnerID   int64
	UserID    int64
	OrgIDs    []int64
	// SourceID 0 means "no source / static": pin a self-contained chart snapshot.
	SourceID    int64
	WorkspaceID int64
	DashboardID int64
	Name        string
	Operation   map[string]any
	ChartSpec   map[string]any
	// Snapshot is the inline payload for a static pin, e.g. {"result": ...}.
	Snapshot map[string]any
}

// PinResult is what Pin returns: the (possibly auto-created) dashboard id and
// the new panel id.
type PinResult struct {
	DashboardID int64 `json:"dashboard_id"`
	PanelID     int64 `json:"panel_id"`
}

// Pin saves the read-only query (recording the origin workspace), ensures the
// owner has a dashboard (creating the default one on first pin), and adds the
// panel. This is the landing point that makes the nav entry appear after the
// first pin.
func (s *DashboardService) Pin(ctx context.Context, in PinInput) (PinResult, error) {
	savedQueryID, err := s.SaveQuery(ctx, SaveQueryInput{
		OwnerType:   in.OwnerType,
		OwnerID:     in.OwnerID,
		UserID:      in.UserID,
		OrgIDs:      in.OrgIDs,
		SourceID:    in.SourceID,
		WorkspaceID: in.WorkspaceID,
		Name:        in.Name,
		Operation:   in.Operation,
		ChartSpec:   in.ChartSpec,
		Snapshot:    in.Snapshot,
	})
	if err != nil {
		return PinResult{}, err
	}

	dashboardID := in.DashboardID
	if dashboardID == 0 {
		dashboardID, err = s.EnsureDefaultDashboard(ctx, in.OwnerType, in.OwnerID, in.UserID, in.OrgIDs)
		if err != nil {
			return PinResult{}, err
		}
	} else {
		// Verify the supplied dashboard is accessible to the caller.
		if _, err := s.getOwnedDashboard(ctx, dashboardID, in.UserID, in.OrgIDs); err != nil {
			return PinResult{}, err
		}
	}

	vizType := "table"
	if t := stringFromOp(in.ChartSpec, "type"); strings.TrimSpace(t) != "" {
		vizType = t
	}
	panelID, err := s.AddPanel(ctx, AddPanelInput{
		DashboardID:  dashboardID,
		SavedQueryID: savedQueryID,
		Title:        in.Name,
		VizType:      vizType,
		ChartSpec:    in.ChartSpec,
	}, in.UserID, in.OrgIDs)
	if err != nil {
		return PinResult{}, err
	}
	return PinResult{DashboardID: dashboardID, PanelID: panelID}, nil
}

// --- row -> DTO helpers ---

func savedQueryRowToDTO(r store.SavedQuery) SavedQueryDTO {
	return SavedQueryDTO{
		ID:          r.ID,
		OwnerType:   r.OwnerType,
		OwnerID:     r.OwnerID,
		SourceID:    r.SourceID.Int64, // 0 when NULL (static saved query)
		WorkspaceID: nullInt64ToPtr(r.WorkspaceID),
		Name:        r.Name,
		Operation:   parseJSONObject(r.Operation),
		ChartSpec:   parseJSONObject(r.ChartSpec),
	}
}

func dashboardRowToDTO(r store.Dashboard) DashboardDTO {
	return DashboardDTO{
		ID:        r.ID,
		OwnerType: r.OwnerType,
		OwnerID:   r.OwnerID,
		Name:      r.Name,
		Layout:    parseJSONObject(r.Layout),
	}
}

func listPanelRowToDTO(r store.ListPanelsWithQueryByDashboardRow) PanelDTO {
	return PanelDTO{
		ID:                 r.ID,
		DashboardID:        r.DashboardID,
		SavedQueryID:       r.SavedQueryID,
		Title:              r.Title,
		VizType:            r.VizType,
		ChartSpec:          parseJSONObject(r.ChartSpec),
		GridX:              r.GridX,
		GridY:              r.GridY,
		GridW:              r.GridW,
		GridH:              r.GridH,
		RefreshIntervalSec: r.RefreshIntervalSec,
		WorkspaceID:        nullInt64ToPtr(r.SqWorkspaceID),
		SourceID:           r.SqSourceID.Int64, // 0 when NULL (static panel)
	}
}

// --- small helpers ---

// staticPanelResult builds the ResultSet for a static (source-less) panel from
// its stored snapshot JSON. If the snapshot carries a "result" object it is
// unmarshalled into a *dataconn.ResultSet; otherwise an empty ResultSet is
// returned. The proxy is never consulted for a static panel.
func staticPanelResult(snapshot string) *dataconn.ResultSet {
	empty := &dataconn.ResultSet{
		Columns:   []dataconn.Column{},
		Rows:      [][]any{},
		Truncated: false,
		Engine:    "",
	}
	if strings.TrimSpace(snapshot) == "" {
		return empty
	}
	var snap struct {
		Result    *dataconn.ResultSet `json:"result"`
		QueriedAt *time.Time          `json:"queried_at"`
	}
	if err := json.Unmarshal([]byte(snapshot), &snap); err != nil {
		return empty
	}
	rs := snap.Result
	if rs == nil {
		rs = empty
	}
	// Prefer the time carried by the result itself (stamped when the chat query
	// ran); fall back to the snapshot-level capture time recorded at pin.
	if rs.QueriedAt == nil {
		rs.QueriedAt = snap.QueriedAt
	}
	return rs
}

// ensureSnapshotQueriedAt guarantees a static snapshot records when its data was
// obtained, so a snapshot panel can show a "data time". It prefers a queried_at
// already carried by the snapshotted result (stamped by the data proxy when the
// chat query ran); otherwise it stamps the pin time as the capture time. The map
// is allocated if nil (e.g. a self-contained echarts block with no result).
func ensureSnapshotQueriedAt(snap map[string]any) map[string]any {
	if snap == nil {
		snap = map[string]any{}
	}
	if _, ok := snap["queried_at"]; ok {
		return snap
	}
	if qa := queriedAtFromResult(snap["result"]); qa != "" {
		snap["queried_at"] = qa
		return snap
	}
	snap["queried_at"] = time.Now().UTC().Format(time.RFC3339)
	return snap
}

// queriedAtFromResult extracts a string queried_at from a snapshot's "result"
// object (a generic map as parsed from JSON), or "" when absent.
func queriedAtFromResult(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	s, _ := m["queried_at"].(string)
	return s
}

// nullInt64FromID maps a 0 id to a NULL (no origin workspace) and any positive
// id to a valid sql.NullInt64. (service.nullInt64 already exists for *int64.)
func nullInt64FromID(v int64) sql.NullInt64 {
	if v <= 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: v, Valid: true}
}

// nullInt64ToPtr converts a sql.NullInt64 to a *int64 (NULL -> nil) for JSON.
func nullInt64ToPtr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

// stringFromOp reads a string field from a loose JSON map.
func stringFromOp(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// paramsFromOp reads the optional "params" array from a saved-query operation.
func paramsFromOp(m map[string]any) []any {
	if m == nil {
		return nil
	}
	if v, ok := m["params"].([]any); ok {
		return v
	}
	return nil
}

// mapFromOp reads a nested JSON object field from a loose operation map.
func mapFromOp(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return nil
}

// stringSliceFromOp reads a JSON array of strings (ignoring non-strings).
func stringSliceFromOp(m map[string]any, key string) []string {
	if m == nil {
		return nil
	}
	arr, ok := m[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// mapSliceFromOp reads a JSON array of objects (e.g. a mongo aggregate pipeline).
func mapSliceFromOp(m map[string]any, key string) []map[string]any {
	if m == nil {
		return nil
	}
	arr, ok := m[key].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, e := range arr {
		if mm, ok := e.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out
}

// dataQueryInputFromOp maps a stored saved-query operation map into the
// structured DataQueryInput fields for ALL source families (SQL statement,
// redis command/args, mongo collection/op/filter/pipeline/document,
// elasticsearch index/method/path/query, http method/path/query/body). The keys
// mirror the pin_query / run_data_query wire shape. Owner / source / workspace
// identity is filled in by the caller; this only carries the operation shape.
func dataQueryInputFromOp(op map[string]any) DataQueryInput {
	return DataQueryInput{
		Statement:    stringFromOp(op, "statement"),
		Params:       paramsFromOp(op),
		Command:      stringFromOp(op, "command"),
		Args:         stringSliceFromOp(op, "args"),
		Collection:   stringFromOp(op, "collection"),
		MongoOp:      stringFromOp(op, "mongo_op"),
		Filter:       mapFromOp(op, "filter"),
		Pipeline:     mapSliceFromOp(op, "pipeline"),
		Document:     mapFromOp(op, "document"),
		Index:        stringFromOp(op, "index"),
		ESMethod:     stringFromOp(op, "es_method"),
		ESPath:       stringFromOp(op, "es_path"),
		Query:        mapFromOp(op, "query"),
		HTTPMethod:   stringFromOp(op, "http_method"),
		HTTPPath:     stringFromOp(op, "http_path"),
		HTTPQuery:    mapFromOp(op, "http_query"),
		HTTPBody:     mapFromOp(op, "http_body"),
		HTTPListPath: stringFromOp(op, "http_list_path"),
	}
}

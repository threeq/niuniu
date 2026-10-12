// Org-wide integration activity — the team-edition half of the audit read
// side (#743). The per-caller feed lives in integration_audit.go; this file
// adds the ONE thing the team edition needs on top of it, per #740: an org
// dimension plus permission control. It deliberately reads the same two audit
// tables through the same shapes rather than growing a second audit stack.
//
// What an org admin gets that a member cannot get from the personal feed:
//   - every member's calls, attributed to a person;
//   - which credential authenticated each call (the org's own, or the
//     member's personal one) -- this is the "用哪份组织凭证" question;
//   - the approval decisions #741 wrote to org_audit_log.
//
// Append-only note: api/data rows come from tables nothing in this codebase
// ever updates or deletes, and the approval rows come from org_audit_log,
// which is likewise append-only. The integration_approval_requests QUEUE is
// not read here precisely because its status legitimately changes; its
// immutable decisions are what org_audit_log holds.
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// ActivityKindApproval is the third kind in the org feed: one approval
// decision, read from org_audit_log.
const ActivityKindApproval = "approval"

// Credential scopes. "org" means the call was authenticated with a credential
// owned by the organization; "user" means the member's own credential.
const (
	ActivityScopeOrg      = "org"
	ActivityScopePersonal = "user"
)

// Unified outcomes. Each kind maps its native status onto these so one filter
// works across the feed.
const (
	ActivityOutcomeOK      = "ok"
	ActivityOutcomeError   = "error"
	ActivityOutcomePending = "pending"
)

// ErrBadActivityOutcome / ErrBadCredentialScope mirror ErrBadActivityKind:
// the handler turns these into 400s instead of silently returning everything.
var (
	ErrBadActivityOutcome = errors.New("invalid activity outcome")
	ErrBadCredentialScope = errors.New("invalid credential scope")
)

// MaxExportRows caps a compliance export. The cap is per source table, so a
// three-kind export tops out at 3x this; it exists so a runaway export cannot
// pin the process, and is far above any realistic org audit volume. A var, not
// a const, so the cap's own test does not have to seed 50k rows.
var MaxExportRows int64 = 50000

// OrgActivityInput is the org-wide feed filter set. The zero value (with OrgID
// set) returns the org's newest activity of every kind across all members.
type OrgActivityInput struct {
	OrgID int64

	Kind string // "", api, data, approval

	MemberID   int64 // > 0 narrows to one member's actions
	ProviderID int64 // > 0 narrows to one provider (implies kind=api)
	SourceID   int64 // > 0 narrows to one data source (implies kind=data)
	ProjectID  int64 // > 0 narrows to the integrations bound to that project

	// CredentialScope narrows by the OWNER of the credential that
	// authenticated the call: ActivityScopeOrg (the organization's
	// credentials), ActivityScopePersonal (the member's own), or "" for both.
	// Ignored by the approval kind, which has no credential.
	CredentialScope string

	// Outcome narrows by result: ok, error, pending, or "" for all. "pending"
	// only ever matches escalated, undecided approvals.
	Outcome string

	From time.Time
	To   time.Time

	Limit  int64
	Offset int64
}

// OrgActivityItem is one row of the org feed. The envelope is shared across
// kinds; kind-specific fields are omitted when they do not apply.
type OrgActivityItem struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	UserID    int64     `json:"user_id"`
	UserName  string    `json:"user_name,omitempty"`
	Outcome   string    `json:"outcome"`
	Status    string    `json:"status"` // display text; see outcomeOf*
	CreatedAt time.Time `json:"created_at"`

	// kind == api
	ProviderID          *int64   `json:"provider_id,omitempty"`
	ProviderName        string   `json:"provider_name,omitempty"`
	CredentialID        int64    `json:"credential_id,omitempty"`
	CredentialAlias     string   `json:"credential_alias,omitempty"`
	CredentialOwnerType string   `json:"credential_owner_type,omitempty"`
	CredentialOwnerID   int64    `json:"credential_owner_id,omitempty"`
	Method              string   `json:"method,omitempty"`
	Path                string   `json:"path,omitempty"`
	StatusCode          *int64   `json:"status_code,omitempty"`
	BodySHA256          string   `json:"body_sha256,omitempty"`
	BodyBytes           int64    `json:"body_bytes,omitempty"`
	BodyFields          []string `json:"body_fields,omitempty"`

	// kind == data
	SourceID         *int64   `json:"source_id,omitempty"`
	SourceName       string   `json:"source_name,omitempty"`
	SourceOwnerType  string   `json:"source_owner_type,omitempty"`
	SourceOwnerID    int64    `json:"source_owner_id,omitempty"`
	AccessMode       string   `json:"access_mode,omitempty"`
	DatabaseName     string   `json:"database_name,omitempty"`
	Objects          []string `json:"objects,omitempty"`
	StatementSummary string   `json:"statement_summary,omitempty"`
	Rows             *int64   `json:"rows,omitempty"`
	DurationMs       *int64   `json:"duration_ms,omitempty"`

	// kind == approval (from org_audit_log; see the file header for why the
	// request queue is not read here)
	Action       string `json:"action,omitempty"`
	ActorLabel   string `json:"actor_label,omitempty"`
	RequestedBy  int64  `json:"requested_by,omitempty"`
	SubjectType  string `json:"subject_type,omitempty"`
	SubjectID    int64  `json:"subject_id,omitempty"`
	SubjectLabel string `json:"subject_label,omitempty"`
	Summary      string `json:"summary,omitempty"`
	Diff         string `json:"diff,omitempty"`
	Note         string `json:"note,omitempty"`
	RequiredRole string `json:"required_role,omitempty"`
}

// OrgActivityPage is one page of the org feed. Total counts every row matching
// the filters across all three sources, before paging.
type OrgActivityPage struct {
	Items  []OrgActivityItem `json:"items"`
	Total  int64             `json:"total"`
	Limit  int64             `json:"limit"`
	Offset int64             `json:"offset"`
}

// activityTables resolves which sources a request reads. Narrowing filters only
// ever REMOVE a source, never add one back over an explicit kind, so
// kind=data plus a provider filter stays empty rather than quietly returning
// api rows. Two rules beyond the personal feed:
//
//   - a provider/source id names one integration, so it also drops the
//     approval side, which belongs to no single integration;
//   - a credential scope names an owner of a credential, so it drops the
//     approval side too: an approval decision was not made "with" a
//     credential, and pretending it was would make the filter lie.
func (in OrgActivityInput) activityTables() (api, data, approval bool, err error) {
	switch in.Kind {
	case "":
		api, data, approval = true, true, true
	case ActivityKindAPI:
		api = true
	case ActivityKindData:
		data = true
	case ActivityKindApproval:
		approval = true
	default:
		return false, false, false, ErrBadActivityKind
	}
	if in.ProviderID > 0 {
		data, approval = false, false
	}
	if in.SourceID > 0 {
		api, approval = false, false
	}
	if in.CredentialScope != "" {
		if in.CredentialScope != ActivityScopeOrg && in.CredentialScope != ActivityScopePersonal {
			return false, false, false, ErrBadCredentialScope
		}
		approval = false
	}
	switch in.Outcome {
	case "", ActivityOutcomeOK, ActivityOutcomeError, ActivityOutcomePending:
	default:
		return false, false, false, ErrBadActivityOutcome
	}
	return api, data, approval, nil
}

// ListOrgActivity returns one page of the org's merged integration activity.
// Callers must already have passed the org-admin gate (Authz.EnsureOrgAuditReadable);
// this method does no permission check of its own.
func (s *IntegrationAuditService) ListOrgActivity(ctx context.Context, in OrgActivityInput) (*OrgActivityPage, error) {
	limit, offset, err := normaliseOrgPage(in)
	if err != nil {
		return nil, err
	}

	items, counts, err := s.collectOrgActivity(ctx, in, offset+limit)
	if err != nil {
		return nil, err
	}

	var total int64
	for _, n := range counts {
		total += n
	}

	page := sliceActivityItems(items, offset, limit)
	s.enrichOrgNames(ctx, page)
	return &OrgActivityPage{Items: page, Total: total, Limit: limit, Offset: offset}, nil
}

// ExportOrgActivity returns EVERY row matching the filters, newest first, so a
// compliance export covers the whole filtered range rather than the page the
// admin happens to be looking at.
//
// It returns the complete set instead of streaming it because the HTTP layer
// must write the CSV header before the first row, and a header already on the
// wire cannot be taken back -- streaming would turn any later failure (a DB
// error, or a cap breach) into a truncated file that still looks valid. Each
// source is capped at MaxExportRows, which also bounds what is buffered here.
func (s *IntegrationAuditService) ExportOrgActivity(ctx context.Context, in OrgActivityInput) ([]OrgActivityItem, error) {
	if _, _, err := normaliseOrgPage(in); err != nil {
		return nil, err
	}
	items, counts, err := s.collectOrgActivity(ctx, in, MaxExportRows+1)
	if err != nil {
		return nil, err
	}
	for _, n := range counts {
		if n > MaxExportRows {
			return nil, ErrExportTooLarge
		}
	}
	return items, nil
}

// ErrExportTooLarge reports that the filtered range exceeds the export cap.
var ErrExportTooLarge = errors.New("export range too large; narrow the filters")

// RecordAuditExport appends an entry to the org's append-only audit log saying
// who pulled the audit trail and with which filters. The export changes
// nothing, but "who read the compliance record" is itself a compliance
// question. Best-effort: a failed write must not fail an export the admin has
// already received.
func (s *IntegrationAuditService) RecordAuditExport(ctx context.Context, orgID, actorUserID int64, actorLabel string, filters map[string]any, rows int) {
	payload, err := json.Marshal(map[string]any{"filters": filters, "rows": rows})
	if err != nil {
		payload = []byte(`{}`)
	}
	if err := s.q.AppendOrgAudit(ctx, store.AppendOrgAuditParams{
		OrgID:       orgID,
		ActorUserID: sql.NullInt64{Int64: actorUserID, Valid: actorUserID != 0},
		ActorLabel:  actorLabel,
		Action:      "integration.audit.exported",
		TargetType:  "integration_audit",
		TargetID:    orgID,
		Payload:     string(payload),
	}); err != nil {
		slog.Warn("integration audit: export audit write failed", "org_id", orgID, "error", err)
	}
}

// normaliseOrgPage applies the same bounds as the personal feed so the two
// read paths cannot drift apart.
func normaliseOrgPage(in OrgActivityInput) (limit, offset int64, err error) {
	limit = in.Limit
	if limit <= 0 {
		limit = defaultActivityLimit
	}
	if limit > maxActivityLimit {
		limit = maxActivityLimit
	}
	offset = in.Offset
	if offset < 0 {
		offset = 0
	}
	_, _, _, err = in.activityTables()
	if err != nil {
		return 0, 0, err
	}
	return limit, offset, nil
}

// collectOrgActivity fetches each wanted source's newest `window` rows, merges
// them newest-first, and returns the merged rows plus the filter-wide count of
// each source.
//
// Fetching the first `window` rows of each source is enough for the merged
// top-`window` (the merged prefix is always contained in the union of the
// per-source prefixes), which keeps every query a single indexed scan both
// SQLite and PostgreSQL plan well.
func (s *IntegrationAuditService) collectOrgActivity(ctx context.Context, in OrgActivityInput, window int64) ([]OrgActivityItem, map[string]int64, error) {
	api, data, approval, err := in.activityTables()
	if err != nil {
		return nil, nil, err
	}

	var fromTS, toTS *time.Time
	if !in.From.IsZero() {
		t := in.From
		fromTS = &t
	}
	if !in.To.IsZero() {
		t := in.To
		toTS = &t
	}

	items := []OrgActivityItem{}
	counts := map[string]int64{}

	if api {
		params := store.ListOrgExternalApiAuditParams{
			OrgID:           in.OrgID,
			MemberID:        in.MemberID,
			ProviderID:      in.ProviderID,
			ProjectID:       in.ProjectID,
			CredentialScope: in.CredentialScope,
			Outcome:         in.Outcome,
			FromTs:          nullTime(fromTS),
			ToTs:            nullTime(toTS),
			Off:             0,
			Lim:             window,
		}
		rows, err := s.q.ListOrgExternalApiAudit(ctx, params)
		if err != nil {
			return nil, nil, err
		}
		n, err := s.q.CountOrgExternalApiAudit(ctx, store.CountOrgExternalApiAuditParams{
			OrgID:           in.OrgID,
			MemberID:        in.MemberID,
			ProviderID:      in.ProviderID,
			ProjectID:       in.ProjectID,
			CredentialScope: in.CredentialScope,
			Outcome:         in.Outcome,
			FromTs:          nullTime(fromTS),
			ToTs:            nullTime(toTS),
		})
		if err != nil {
			return nil, nil, err
		}
		counts[ActivityKindAPI] = n
		for _, r := range rows {
			items = append(items, orgAPIActivityItem(r))
		}
	}

	if data {
		rows, err := s.q.ListOrgDataSourceAudit(ctx, store.ListOrgDataSourceAuditParams{
			OrgID:           in.OrgID,
			MemberID:        in.MemberID,
			SourceID:        in.SourceID,
			ProjectID:       in.ProjectID,
			CredentialScope: in.CredentialScope,
			Outcome:         in.Outcome,
			FromTs:          nullTime(fromTS),
			ToTs:            nullTime(toTS),
			Off:             0,
			Lim:             window,
		})
		if err != nil {
			return nil, nil, err
		}
		n, err := s.q.CountOrgDataSourceAudit(ctx, store.CountOrgDataSourceAuditParams{
			OrgID:           in.OrgID,
			MemberID:        in.MemberID,
			SourceID:        in.SourceID,
			ProjectID:       in.ProjectID,
			CredentialScope: in.CredentialScope,
			Outcome:         in.Outcome,
			FromTs:          nullTime(fromTS),
			ToTs:            nullTime(toTS),
		})
		if err != nil {
			return nil, nil, err
		}
		counts[ActivityKindData] = n
		for _, r := range rows {
			items = append(items, orgDataActivityItem(r))
		}
	}

	if approval {
		rows, err := s.q.ListOrgApprovalAudit(ctx, store.ListOrgApprovalAuditParams{
			OrgID:     in.OrgID,
			MemberID:  in.MemberID,
			ProjectID: in.ProjectID,
			Outcome:   in.Outcome,
			FromTs:    nullTime(fromTS),
			ToTs:      nullTime(toTS),
			Off:       0,
			Lim:       window,
		})
		if err != nil {
			return nil, nil, err
		}
		n, err := s.q.CountOrgApprovalAudit(ctx, store.CountOrgApprovalAuditParams{
			OrgID:     in.OrgID,
			MemberID:  in.MemberID,
			ProjectID: in.ProjectID,
			Outcome:   in.Outcome,
			FromTs:    nullTime(fromTS),
			ToTs:      nullTime(toTS),
		})
		if err != nil {
			return nil, nil, err
		}
		counts[ActivityKindApproval] = n
		for _, r := range rows {
			items = append(items, approvalActivityItem(r))
		}
	}

	// Newest first, id breaking same-timestamp ties. The window-fetch argument
	// above is only exact if each source is queried in the SAME order it is
	// merged in, which is why every List* query orders by (created_at DESC,
	// id DESC) -- ordering by id alone would be equivalent only while ids
	// happen to track time, and a single back-dated row would then let a page
	// boundary drop a row. Ids are comparable only within a kind, so the
	// timestamp is the cross-kind key and the id tiebreak is per kind, which is
	// stable because the merged slice is a deterministic function of the rows.
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.After(items[j].CreatedAt)
		}
		return items[i].ID > items[j].ID
	})
	return items, counts, nil
}

// sliceActivityItems returns items[offset:offset+limit] as a non-nil slice.
func sliceActivityItems(items []OrgActivityItem, offset, limit int64) []OrgActivityItem {
	if offset >= int64(len(items)) {
		return []OrgActivityItem{}
	}
	end := offset + limit
	if end > int64(len(items)) {
		end = int64(len(items))
	}
	return items[offset:end]
}

func orgAPIActivityItem(r store.ListOrgExternalApiAuditRow) OrgActivityItem {
	pid, code := r.ProviderID, r.StatusCode
	outcome, status := outcomeOfHTTPStatus(r.StatusCode)
	it := OrgActivityItem{
		ID:                  r.ID,
		Kind:                ActivityKindAPI,
		UserID:              r.UserID,
		Outcome:             outcome,
		Status:              status,
		CreatedAt:           r.CreatedAt,
		ProviderID:          &pid,
		CredentialID:        r.CredentialID,
		CredentialAlias:     r.CredentialAlias,
		CredentialOwnerType: r.CredentialOwnerType,
		CredentialOwnerID:   r.CredentialOwnerID,
		Method:              r.Method,
		Path:                r.Path,
		StatusCode:          &code,
		BodySHA256:          r.BodySha256,
		BodyBytes:           r.BodyBytes,
	}
	var fields []string
	if err := json.Unmarshal([]byte(r.BodyFields), &fields); err == nil && len(fields) > 0 {
		it.BodyFields = fields
	}
	return it
}

func orgDataActivityItem(r store.ListOrgDataSourceAuditRow) OrgActivityItem {
	sid, n, ms := r.SourceID, r.Rows, r.DurationMs
	outcome, status := outcomeOfDataStatus(r.Status)
	it := OrgActivityItem{
		ID:               r.ID,
		Kind:             ActivityKindData,
		UserID:           r.UserID,
		Outcome:          outcome,
		Status:           status,
		CreatedAt:        r.CreatedAt,
		SourceID:         &sid,
		SourceName:       r.SourceName,
		SourceOwnerType:  r.SourceOwnerType,
		SourceOwnerID:    r.SourceOwnerID,
		AccessMode:       r.AccessMode,
		DatabaseName:     r.DatabaseName,
		StatementSummary: r.StatementSummary,
		Rows:             &n,
		DurationMs:       &ms,
	}
	var objs []string
	if err := json.Unmarshal([]byte(r.Objects), &objs); err == nil && len(objs) > 0 {
		it.Objects = objs
	}
	return it
}

func approvalActivityItem(r store.OrgAuditLog) OrgActivityItem {
	outcome, status := outcomeOfApprovalAction(r.Action)
	it := OrgActivityItem{
		ID:         r.ID,
		Kind:       ActivityKindApproval,
		Outcome:    outcome,
		Status:     status,
		CreatedAt:  r.CreatedAt,
		Action:     r.Action,
		ActorLabel: r.ActorLabel,
	}
	if r.ActorUserID.Valid {
		it.UserID = r.ActorUserID.Int64
	}
	it.SubjectType = r.TargetType
	it.SubjectID = r.TargetID

	// The payload is written by #741's approval audit and holds the decision
	// context: requester, subject, the diff that was approved, and the note.
	// A row whose payload predates or postdates that shape still renders --
	// the envelope above is enough on its own.
	var p approvalAuditPayload
	if err := json.Unmarshal([]byte(r.Payload), &p); err == nil {
		it.RequestedBy = p.RequestedBy
		it.SubjectType = p.SubjectType
		if p.SubjectID != 0 {
			it.SubjectID = p.SubjectID
		}
		it.SubjectLabel = p.SubjectLabel
		it.Summary = p.Summary
		it.Diff = p.Diff
		it.Note = p.Note
		it.RequiredRole = p.RequiredRole
	}
	return it
}

// outcomeOfHTTPStatus maps a proxy call's HTTP status onto the unified outcome
// vocabulary. A zero status never reached a response, so it is reported as an
// error rather than a success.
func outcomeOfHTTPStatus(code int64) (outcome, status string) {
	if code > 0 && code < 400 {
		return ActivityOutcomeOK, "ok"
	}
	return ActivityOutcomeError, "error"
}

// outcomeOfDataStatus maps the data proxy's own status vocabulary.
func outcomeOfDataStatus(s string) (outcome, status string) {
	if s == "ok" {
		return ActivityOutcomeOK, "ok"
	}
	if s == "" {
		return ActivityOutcomeError, "error"
	}
	return ActivityOutcomeError, s
}

// outcomeOfApprovalAction maps an approval audit action onto the unified
// vocabulary: a granted request is ok, a refused or withdrawn one is an error,
// and an escalation is still open.
func outcomeOfApprovalAction(action string) (outcome, status string) {
	switch action {
	case "integration.approval.approved":
		return ActivityOutcomeOK, "approved"
	case "integration.approval.denied":
		return ActivityOutcomeError, "denied"
	case "integration.approval.cancelled":
		return ActivityOutcomeError, "cancelled"
	case "integration.approval.invalidated":
		return ActivityOutcomeError, "invalidated"
	case "integration.approval.escalated":
		return ActivityOutcomePending, "escalated"
	default:
		return ActivityOutcomePending, action
	}
}

// enrichOrgNames fills the display names for a page's rows: the acting member,
// the provider, the data source. Names are resolved per page and only for ids
// actually present. A lookup failure is not fatal -- the row keeps its id.
func (s *IntegrationAuditService) enrichOrgNames(ctx context.Context, items []OrgActivityItem) {
	if len(items) == 0 {
		return
	}
	var userIDs, providerIDs, sourceIDs []int64
	seenU, seenP, seenS := map[int64]bool{}, map[int64]bool{}, map[int64]bool{}
	for _, it := range items {
		if it.UserID != 0 && !seenU[it.UserID] {
			seenU[it.UserID] = true
			userIDs = append(userIDs, it.UserID)
		}
		if it.ProviderID != nil && !seenP[*it.ProviderID] {
			seenP[*it.ProviderID] = true
			providerIDs = append(providerIDs, *it.ProviderID)
		}
		if it.SourceID != nil && !seenS[*it.SourceID] {
			seenS[*it.SourceID] = true
			sourceIDs = append(sourceIDs, *it.SourceID)
		}
	}

	userNames := map[int64]string{}
	if len(userIDs) > 0 {
		if rows, err := s.q.ListUserNamesByIDs(ctx, userIDs); err == nil {
			for _, r := range rows {
				if r.DisplayName != "" {
					userNames[r.ID] = r.DisplayName
				} else {
					userNames[r.ID] = r.Username
				}
			}
		}
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

	// Data rows already carry their source name from the org query's join;
	// only rows that somehow lack one need the fallback lookup.
	var missingSources []int64
	for _, it := range items {
		if it.SourceID != nil && it.SourceName == "" {
			missingSources = append(missingSources, *it.SourceID)
		}
	}
	sourceNames := map[int64]string{}
	if len(missingSources) > 0 {
		if rows, err := s.q.ListDataSourceNamesByIDs(ctx, missingSources); err == nil {
			for _, r := range rows {
				sourceNames[r.ID] = r.Name
			}
		}
	}

	for i := range items {
		if items[i].UserID != 0 {
			items[i].UserName = userNames[items[i].UserID]
		}
		if items[i].ProviderID != nil {
			items[i].ProviderName = providerNames[*items[i].ProviderID]
		}
		if items[i].SourceID != nil && items[i].SourceName == "" {
			items[i].SourceName = sourceNames[*items[i].SourceID]
		}
	}
}

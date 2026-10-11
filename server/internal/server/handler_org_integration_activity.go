// handler_org_integration_activity.go — the team-edition audit view (#743).
//
// Routes served (registered in router.go, inside the license-gated /orgs
// group so a personal build never exposes them):
//
//	GET /api/orgs/:id/integration-activity           org-wide merged feed
//	GET /api/orgs/:id/integration-activity/export    same filters, CSV
//
// Both are admin-only. The gate lives in the service layer
// (Authz.EnsureOrgAuditReadable) and is applied here on every request: hiding
// the tab in the SPA is convenience, the API is authoritative.
//
// The feed itself is read through the SAME service the personal view (#728)
// uses — IntegrationAuditService — which is the point of #740's "don't build a
// second audit stack" rule.
package server

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// orgAuditScope resolves :id, requires an authenticated caller, and enforces
// the org-admin gate. It writes the error response itself and reports whether
// the caller may continue.
func (s *Server) orgAuditScope(c *gin.Context) (orgID, callerID int64, ok bool) {
	callerID = c.GetInt64("auth_user_id")
	if callerID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "authentication required"}})
		return 0, 0, false
	}
	orgID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || orgID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "invalid org ID"}})
		return 0, 0, false
	}
	if err := s.authzSvc.EnsureOrgAuditReadable(c.Request.Context(), callerID, orgID); err != nil {
		if errors.Is(err, service.ErrForbidden) {
			// Deliberately 403, not 404: the caller is a real user asking for
			// a real org, they simply may not read other members' activity.
			c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"code": "FORBIDDEN", "message": "需要组织管理员权限才能查看跨用户审计"}})
			return 0, 0, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL_ERROR", "message": err.Error()}})
		return 0, 0, false
	}
	return orgID, callerID, true
}

// writeActivityErr maps the feed's sentinel errors onto status codes.
func writeActivityErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrBadActivityKind):
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "kind must be api, data or approval"}})
	case errors.Is(err, service.ErrBadActivityOutcome):
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "outcome must be ok, error or pending"}})
	case errors.Is(err, service.ErrBadCredentialScope):
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "credential_scope must be org or user"}})
	case errors.Is(err, service.ErrExportTooLarge):
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "结果集过大，请缩小时间范围或增加过滤条件"}})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL_ERROR", "message": err.Error()}})
	}
}

// parseOrgActivityInput reads the shared filter query string. It mirrors the
// personal feed's parser so the two views take exactly the same parameters.
func parseOrgActivityInput(c *gin.Context, orgID int64) (service.OrgActivityInput, bool) {
	bad := func(msg string) (service.OrgActivityInput, bool) {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": msg}})
		return service.OrgActivityInput{}, false
	}

	in := service.OrgActivityInput{
		OrgID:           orgID,
		Kind:            c.Query("kind"),
		CredentialScope: c.Query("credential_scope"),
		Outcome:         c.Query("outcome"),
	}

	for _, f := range []struct {
		name string
		dst  *int64
	}{
		{"member_id", &in.MemberID},
		{"provider_id", &in.ProviderID},
		{"source_id", &in.SourceID},
		{"project_id", &in.ProjectID},
		{"limit", &in.Limit},
		{"offset", &in.Offset},
	} {
		raw := c.Query(f.name)
		if raw == "" {
			continue
		}
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 0 {
			return bad(f.name + " must be a non-negative integer")
		}
		*f.dst = v
	}

	var err error
	if in.From, err = parseActivityTime(c.Query("from"), false); err != nil {
		return bad("from must be RFC3339 or YYYY-MM-DD")
	}
	if in.To, err = parseActivityTime(c.Query("to"), true); err != nil {
		return bad("to must be RFC3339 or YYYY-MM-DD")
	}
	return in, true
}

// orgIntegrationActivity handles GET /api/orgs/:id/integration-activity.
func (s *Server) orgIntegrationActivity(c *gin.Context) {
	orgID, _, ok := s.orgAuditScope(c)
	if !ok {
		return
	}
	in, ok := parseOrgActivityInput(c, orgID)
	if !ok {
		return
	}
	page, err := s.integrationAuditSvc.ListOrgActivity(c.Request.Context(), in)
	if err != nil {
		writeActivityErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": page})
}

// csvHeader is the export's column set: the four questions the view answers
// (who / when / what / result) plus the fingerprint and credential
// attribution that make an exported row self-explanatory offline.
var csvHeader = []string{
	"created_at", "kind", "outcome", "status", "user_id", "user_name",
	"target", "credential_scope", "credential_alias",
	"action", "result", "rows", "duration_ms",
	"body_sha256", "body_bytes", "body_fields", "note",
}

// csvRow flattens one feed item into the header's column order.
func csvRow(it service.OrgActivityItem) []string {
	row := make([]string, len(csvHeader))
	row[0] = it.CreatedAt.UTC().Format(time.RFC3339)
	row[1] = it.Kind
	row[2] = it.Outcome
	row[3] = it.Status
	row[4] = strconv.FormatInt(it.UserID, 10)
	row[5] = it.UserName

	switch it.Kind {
	case service.ActivityKindAPI:
		row[6] = it.ProviderName
		row[7] = credentialScopeLabel(it.CredentialOwnerType)
		row[8] = it.CredentialAlias
		row[9] = it.Method + " " + it.Path
		if it.StatusCode != nil {
			row[10] = strconv.FormatInt(*it.StatusCode, 10)
		}
		row[13] = it.BodySHA256
		if it.BodyBytes != 0 {
			row[14] = strconv.FormatInt(it.BodyBytes, 10)
		}
		if len(it.BodyFields) > 0 {
			row[15] = joinFields(it.BodyFields)
		}
	case service.ActivityKindData:
		row[6] = it.SourceName
		row[7] = credentialScopeLabel(it.SourceOwnerType)
		row[9] = it.StatementSummary
		if it.Rows != nil {
			row[11] = strconv.FormatInt(*it.Rows, 10)
		}
		if it.DurationMs != nil {
			row[12] = strconv.FormatInt(*it.DurationMs, 10)
		}
	case service.ActivityKindApproval:
		row[6] = it.SubjectLabel
		row[9] = it.Action
		row[10] = it.Status
		row[16] = firstNonEmpty(it.Note, it.Summary)
	}
	return row
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// credentialScopeLabel renders the credential owner as the export's scope
// column. An unattributed row (credential predates the column, or the row is
// an approval) is left blank rather than labelled "user", which would be a
// claim the data does not support.
func credentialScopeLabel(ownerType string) string {
	switch ownerType {
	case service.ActivityScopeOrg:
		return service.ActivityScopeOrg
	case service.ActivityScopePersonal:
		return "personal"
	default:
		return ""
	}
}

// joinFields renders the fingerprint's field-name list for a CSV cell. The
// values themselves are never stored, so there is nothing sensitive to quote.
func joinFields(fields []string) string {
	out := ""
	for i, f := range fields {
		if i > 0 {
			out += "|"
		}
		out += f
	}
	return out
}

// orgIntegrationActivityExport handles
// GET /api/orgs/:id/integration-activity/export — the same feed as CSV, for
// compliance archival. The row cap is checked BEFORE any bytes are written,
// because a CSV header cannot be un-sent once flushed.
func (s *Server) orgIntegrationActivityExport(c *gin.Context) {
	orgID, callerID, ok := s.orgAuditScope(c)
	if !ok {
		return
	}
	in, ok := parseOrgActivityInput(c, orgID)
	if !ok {
		return
	}

	counts, err := s.integrationAuditSvc.OrgActivityCounts(c.Request.Context(), in)
	if err != nil {
		writeActivityErr(c, err)
		return
	}
	for _, n := range counts {
		if n > service.MaxExportRows {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "结果集过大，请缩小时间范围或增加过滤条件"}})
			return
		}
	}

	label, _ := c.Get("auth_username")
	labelStr, _ := label.(string)

	filename := fmt.Sprintf("integration-activity-org%d-%s.csv", orgID, time.Now().UTC().Format("20060102T150405Z"))
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	// UTF-8 BOM so Excel opens the Chinese headers and usernames correctly.
	_, _ = c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})

	w := csv.NewWriter(c.Writer)
	_ = w.Write(csvHeader)

	rows := 0
	// A write error here means the client went away mid-download; there is no
	// way to report it through the (already committed) response, so it is
	// surfaced only to the caller's own stream.
	_ = s.integrationAuditSvc.ExportOrgActivity(c.Request.Context(), in, func(it service.OrgActivityItem) error {
		rows++
		return w.Write(csvRow(it))
	})
	w.Flush()

	filters := map[string]any{
		"kind":             in.Kind,
		"member_id":        in.MemberID,
		"provider_id":      in.ProviderID,
		"source_id":        in.SourceID,
		"project_id":       in.ProjectID,
		"credential_scope": in.CredentialScope,
		"outcome":          in.Outcome,
	}
	if !in.From.IsZero() {
		filters["from"] = in.From.UTC().Format(time.RFC3339)
	}
	if !in.To.IsZero() {
		filters["to"] = in.To.UTC().Format(time.RFC3339)
	}
	s.integrationAuditSvc.RecordAuditExport(c.Request.Context(), orgID, callerID, labelStr, filters, rows)
}

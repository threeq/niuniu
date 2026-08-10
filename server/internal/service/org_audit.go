package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// appendResourceAudit writes an org_audit_log entry for a mutation on an
// org-owned resource. It is a no-op (returns nil) when ownerType == "user"
// because personal resources do not have an audit trail.
//
// Parameters:
//   - tx may be nil; in that case the write uses db directly (best-effort).
//   - actorUserID == 0 means a system action (scheduler, migration, etc.).
//   - action follows the pattern <resource>.<verb>, e.g. "project.created".
//   - targetType is the resource kind, e.g. "project", "workspace".
//   - targetID is the resource primary key.
//   - payloadJSON is a JSON string with diagnostically useful fields.
//
// db is *store.DB (not *sql.DB) so the sqlc Queries instance is built with
// PgDB-aware wrapping on Postgres. Same for *store.Tx. Using the bare
// store.New(rawDB) here would silently hit "syntax error at end of input"
// the moment the first project/workspace gets created on a PG-backed deploy.
func appendResourceAudit(
	ctx context.Context,
	db *store.DB,
	tx *store.Tx,
	ownerType string,
	ownerID int64,
	actorUserID int64,
	action string,
	targetType string,
	targetID int64,
	payloadJSON string,
) {
	if ownerType != "org" {
		// No audit trail for personal resources.
		return
	}
	if db == nil && tx == nil {
		// No DB available — skip silently (e.g. unit tests).
		return
	}

	params := store.AppendOrgAuditParams{
		OrgID:       ownerID,
		ActorUserID: sql.NullInt64{Int64: actorUserID, Valid: actorUserID != 0},
		ActorLabel:  "", // populated by OrgService.appendAudit; here we leave it empty
		Action:      action,
		TargetType:  targetType,
		TargetID:    targetID,
		Payload:     payloadJSON,
	}

	var execErr error
	if tx != nil {
		execErr = tx.Queries().AppendOrgAudit(ctx, params)
	} else {
		execErr = db.Queries().AppendOrgAudit(ctx, params)
	}
	if execErr != nil {
		// Audit writes are best-effort; log but don't propagate.
		slog.Warn("appendResourceAudit: write failed",
			"action", action, "targetType", targetType, "targetID", targetID, "err", execErr)
	}
}

// resourceAuditPayload builds a minimal JSON payload string for audit entries.
func resourceAuditPayload(name string, extras ...string) string {
	payload := fmt.Sprintf(`{"name":%q`, name)
	for i := 0; i+1 < len(extras); i += 2 {
		payload += fmt.Sprintf(`,%q:%q`, extras[i], extras[i+1])
	}
	payload += "}"
	return payload
}

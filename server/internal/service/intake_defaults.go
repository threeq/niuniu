package service

import (
	"context"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// EnsureIntakeProjectDefaults seeds one intake_settings row per known source for
// a project. The settings view renders the schema default (observe) for a
// missing row, while imbotPolicy.LoadPolicy fails closed to off — without the
// stored rows the UI promises observation the runtime never performs.
func EnsureIntakeProjectDefaults(ctx context.Context, db *store.DB, projectID int64) error {
	if db == nil {
		return nil
	}
	for _, kind := range intakeSourceKinds {
		if _, err := db.ExecContext(ctx,
			`INSERT OR IGNORE INTO intake_settings
				(project_id, source_kind, mode, daily_cap, allow_list_json, bot_list_json)
			 VALUES (?, ?, ?, ?, '[]', '[]')`,
			projectID, kind, defaultIntakeMode, defaultIntakeDailyCap,
		); err != nil {
			return err
		}
	}
	return nil
}

// BackfillIntakeProjectDefaults seeds projects created before intake settings
// existed. INSERT OR IGNORE preserves any source the user already configured.
func BackfillIntakeProjectDefaults(ctx context.Context, db *store.DB) error {
	if db == nil {
		return nil
	}
	for _, kind := range intakeSourceKinds {
		if _, err := db.ExecContext(ctx,
			`INSERT OR IGNORE INTO intake_settings
				(project_id, source_kind, mode, daily_cap, allow_list_json, bot_list_json)
			 SELECT id, ?, ?, ?, '[]', '[]' FROM projects`,
			kind, defaultIntakeMode, defaultIntakeDailyCap,
		); err != nil {
			return err
		}
	}
	return nil
}

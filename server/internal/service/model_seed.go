package service

import (
	"context"
	"database/sql"
	"errors"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// builtinModels are the system-wide model names seeded under the owner_id=0
// sentinel so every owner sees them without configuring anything. Adding or
// removing one here is a product decision; SeedBuiltinModels is idempotent, so
// an existing row (including a user modification) is never overwritten.
var builtinModels = []string{
	"glm-5.1", "glm-5-turbo", "glm-4.5-air",
	"deepseek-v4-pro", "deepseek-v4-flash",
	"qwen3.6-plus",
	"kimi-k2.6",
	"MiniMax-M2.7",
}

// SeedBuiltinModels inserts the system model library rows
// (owner_type='user', owner_id=0, source='builtin'). Rows are matched by name;
// an existing row is left untouched, so this is safe to run on every startup
// and never overwrites an owner's edits or a previously seeded row.
func SeedBuiltinModels(ctx context.Context, q *store.Queries) error {
	for _, name := range builtinModels {
		existing, err := q.GetModelByName(ctx, store.GetModelByNameParams{
			OwnerType: "user", OwnerID: 0, Name: name,
		})
		if err == nil && existing.ID > 0 {
			continue
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := q.CreateModel(ctx, store.CreateModelParams{
			OwnerType:   "user",
			OwnerID:     0,
			Name:        name,
			DisplayName: name,
			Enabled:     1,
			Source:      "builtin",
		}); err != nil {
			return err
		}
	}
	return nil
}

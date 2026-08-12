package service

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// EnvAccountService manages subscription-platform accounts (name + API key).
// Env presets reference an account via a "${ACCOUNT:<name>}" placeholder in an
// env value; sceneenv.Resolve substitutes the account's api_key at spawn time.
// One account can back many presets and many agents — changing the key once
// updates everywhere it is referenced.
type EnvAccountService struct {
	q     *store.Queries
	db    *store.DB
	authz *Authz
}

func NewEnvAccountService(q *store.Queries, db *sql.DB, authz *Authz) *EnvAccountService {
	return &EnvAccountService{q: q, db: store.Wrap(db), authz: authz}
}

func (s *EnvAccountService) List(ctx context.Context) ([]store.EnvAccount, error) {
	return s.q.ListEnvAccounts(ctx)
}

// ListForUser returns accounts accessible to userID (personal + org
// memberships, plus system-wide owner_id=0 rows).
func (s *EnvAccountService) ListForUser(ctx context.Context, userID int64) ([]store.EnvAccount, error) {
	owners, err := s.authz.Accessible(ctx, userID)
	if err != nil {
		return nil, err
	}
	orgIDs := owners.OrgIDs
	if len(orgIDs) == 0 {
		orgIDs = []int64{-1}
	}
	return s.q.ListEnvAccountsForOwners(ctx, store.ListEnvAccountsForOwnersParams{
		OwnerID: owners.UserID,
		OrgIds:  orgIDs,
	})
}

func (s *EnvAccountService) Get(ctx context.Context, id int64) (store.EnvAccount, error) {
	return s.q.GetEnvAccount(ctx, id)
}

func (s *EnvAccountService) Create(ctx context.Context, name, platform, description, apiKey string, ownerType string, ownerID int64) (store.EnvAccount, error) {
	return s.q.CreateEnvAccount(ctx, store.CreateEnvAccountParams{
		Name:        name,
		Platform:    platform,
		Description: description,
		ApiKey:      apiKey,
		OwnerType:   ownerType,
		OwnerID:     ownerID,
	})
}

func (s *EnvAccountService) Update(ctx context.Context, id int64, name, platform, description, apiKey string) error {
	return s.q.UpdateEnvAccount(ctx, store.UpdateEnvAccountParams{
		ID:          id,
		Name:        name,
		Platform:    platform,
		Description: description,
		ApiKey:      apiKey,
	})
}

func (s *EnvAccountService) Delete(ctx context.Context, id int64) error {
	return s.q.DeleteEnvAccount(ctx, id)
}

// SeedDefaults seeds a small set of system-wide (owner_id=0) accounts for the
// platforms the default env presets reference, so a fresh install has somewhere
// to put a real API key. Accounts are created only if their name is absent;
// existing rows (including user-edited ones) are never overwritten.
func (s *EnvAccountService) SeedDefaults(ctx context.Context) error {
	accounts, err := s.q.ListEnvAccounts(ctx)
	if err != nil {
		return err
	}
	existing := make(map[string]bool, len(accounts))
	for _, a := range accounts {
		existing[a.Name] = true
	}

	defaults := []store.CreateEnvAccountParams{
		{Name: "DeepSeek", Platform: "deepseek", Description: "DeepSeek V4 API Key"},
		{Name: "智谱", Platform: "zhipu", Description: "智谱 GLM 系列 API Key"},
		{Name: "MiniMax", Platform: "minimax", Description: "MiniMax M2.7 API Key"},
		{Name: "通义千问", Platform: "qwen", Description: "通义千问 DashScope API Key"},
		{Name: "Kimi", Platform: "moonshot", Description: "Kimi 月之暗面 API Key"},
		{Name: "火山方舟", Platform: "volcengine-ark", Description: "火山方舟 API Key"},
	}
	for _, d := range defaults {
		if existing[d.Name] {
			continue
		}
		// owner_type='user' + owner_id=0 marks the row system-wide while
		// passing the schema CHECK constraint (mirrors env_presets seeding).
		d.OwnerType = "user"
		d.OwnerID = 0
		if _, err := s.q.CreateEnvAccount(ctx, d); err != nil {
			slog.Warn("seed env account failed", "name", d.Name, "error", err)
		}
	}
	return nil
}
package server

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// backfillBuiltinPackVersions advances Ops Bots that are pinned to an older
// builtin pack version to the newest embedded seed version.
//
// InstallBuiltins never overwrites an installed id/version (user edits to an
// installed pack must survive), so a shipped recipe fix would otherwise never
// reach a bot whose row pins the old version. Builtin packs are product-owned:
// bots are only advanced when their pinned version is itself a builtin seed
// version, so a user-generated/imported version keeps its pin.
//
// Runs once at startup. Failures are logged and skipped — one unreadable owner
// directory must not block server startup.
func (s *Server) backfillBuiltinPackVersions(ctx context.Context) {
	if s == nil || s.opsPackRegistry == nil || s.queries == nil || s.cfg.DataDir == "" {
		return
	}
	for _, ownerDir := range []struct{ base, kind string }{
		{"users", "user"},
		{"orgs", "org"},
	} {
		entries, err := os.ReadDir(filepath.Join(s.cfg.DataDir, ownerDir.base))
		if err != nil {
			if !os.IsNotExist(err) {
				slog.Warn("opsbot: backfill builtin pack versions: read owner dir",
					"dir", ownerDir.base, "error", err)
			}
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			ownerID, err := strconv.ParseInt(entry.Name(), 10, 64)
			if err != nil || ownerID <= 0 {
				continue
			}
			bots, err := s.queries.ListOpsBotsByOwner(ctx, store.ListOpsBotsByOwnerParams{
				OwnerType: ownerDir.kind,
				OwnerID:   ownerID,
			})
			if err != nil {
				slog.Warn("opsbot: backfill builtin pack versions: list bots",
					"owner", entry.Name(), "error", err)
				continue
			}
			for _, bot := range bots {
				if bot.PackVersion <= 0 {
					continue // already follows the latest published version
				}
				latest, ok := packs.LatestBuiltinVersion(bot.PackID)
				if !ok || int64(latest) <= bot.PackVersion {
					continue
				}
				if !packs.IsBuiltinVersion(bot.PackID, int(bot.PackVersion)) {
					continue // a user-generated/imported version: keep the pin
				}
				if err := packs.InstallBuiltins(s.opsPackRegistry, ownerDir.kind, ownerID); err != nil {
					slog.Warn("opsbot: backfill builtin pack versions: install",
						"pack", bot.PackID, "error", err)
					continue
				}
				if _, err := s.queries.UpdateOpsBotConfig(ctx, store.UpdateOpsBotConfigParams{
					DisplayName:        bot.DisplayName,
					PersonaJson:        bot.PersonaJson,
					CredentialRefsJson: bot.CredentialRefsJson,
					KnowledgeScopeJson: bot.KnowledgeScopeJson,
					GoalsJson:          bot.GoalsJson,
					CadenceJson:        bot.CadenceJson,
					ApprovalPolicyJson: bot.ApprovalPolicyJson,
					BudgetJson:         bot.BudgetJson,
					PackVersion:        int64(latest),
					ID:                 bot.ID,
				}); err != nil {
					slog.Warn("opsbot: backfill builtin pack versions: update bot",
						"bot", bot.ID, "error", err)
					continue
				}
				slog.Info("opsbot: advanced bot to newer builtin pack",
					"bot", bot.ID, "pack", bot.PackID,
					"from", bot.PackVersion, "to", latest)
			}
		}
	}
}

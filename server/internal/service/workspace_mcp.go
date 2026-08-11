package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// CurrentMCPServers returns the list of extra MCP server names persisted on
// the workspace row (always parsed from mcp_servers JSON; nil if empty/null).
func (s *WorkspaceService) CurrentMCPServers(ctx context.Context, wsID int64) ([]string, error) {
	ws, err := s.q.GetWorkspace(ctx, wsID)
	if err != nil {
		return nil, err
	}
	return parseMCPServersJSON(ws.McpServers)
}

// parseMCPServersJSON decodes the JSON-as-string mcp_servers column into a
// `[]string`. Always returns a non-nil slice for valid input (including empty
// arrays / null literal), so callers can pass the result directly into
// `MCPConfigGenerator.Generate(... extras=...)` without accidentally hitting
// the legacy-compat branch (which is keyed on extras==nil). The only way to
// produce a nil-extras call is to never invoke this helper — i.e. the agent
// spawn path on a row that has never been persisted by the UI.
func parseMCPServersJSON(raw string) ([]string, error) {
	if raw == "" || raw == "null" {
		return []string{}, nil
	}
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		return nil, fmt.Errorf("decode mcp_servers: %w", err)
	}
	if names == nil {
		names = []string{}
	}
	return names, nil
}

// ResolveWorkspaceRepoPaths returns local clone paths of every repository
// attached to the given workspace. Used by the MCP redetect endpoint so the
// detector can scan the same repos the agent will work on.
func (s *WorkspaceService) ResolveWorkspaceRepoPaths(ctx context.Context, wsID int64) ([]string, error) {
	ws, err := s.q.GetWorkspace(ctx, wsID)
	if err != nil {
		return nil, err
	}
	worktrees, err := s.q.ListWorktrees(ctx, wsID)
	if err != nil {
		return nil, err
	}
	owner := OwnerRef{Type: ws.OwnerType, ID: ws.OwnerID}
	paths := make([]string, 0, len(worktrees))
	for _, wt := range worktrees {
		paths = append(paths, owner.RepositoryPath(s.dataDir, wt.RepositoryID))
	}
	return paths, nil
}

// ResolveRepoPathsForUser checks per-repo authz and returns local clone paths.
// Used by the wizard's pre-create detect endpoint.
func (s *WorkspaceService) ResolveRepoPathsForUser(ctx context.Context, userID int64, repoIDs []int64) ([]string, error) {
	paths := make([]string, 0, len(repoIDs))
	for _, id := range repoIDs {
		owner, err := s.authz.CanAccessRepository(ctx, userID, id)
		if err != nil {
			return nil, err
		}
		paths = append(paths, owner.RepositoryPath(s.dataDir, id))
	}
	return paths, nil
}

// UpdateMCPServers persists a new MCP server list for the workspace and rewrites
// the active CLI config via the generator's stage-and-swap path.
//
// Order:
//  1. snapshot the existing .mcp.json bytes into a rollback buffer
//  2. write the new .mcp.json (generator does stage-and-swap)
//  3. UPDATE DB
//  4. if DB UPDATE fails, restore the snapshot via os.WriteFile (best-effort)
//
// File-side stage-and-swap is fully atomic. Step 4 is NOT atomic — if the
// process dies between "new file is in place" and "rollback write completes"
// the .mcp.json will be the new version while the DB row still has the old
// list. The window is microseconds and only opens on DB UPDATE failure
// (rare); the next normal call to UpdateMCPServers will re-converge.
//
// Addresses code review I6.
func (s *WorkspaceService) UpdateMCPServers(ctx context.Context, wsID int64, servers []string) (*MCPGenerateResult, error) {
	ws, err := s.q.GetWorkspace(ctx, wsID)
	if err != nil {
		return nil, fmt.Errorf("load workspace: %w", err)
	}
	if servers == nil {
		servers = []string{}
	}

	// Scene-aware path: when the scene layer/projector services are wired,
	// route the write through the implicit base layer so the projection stack
	// stays the single source of truth (spec §2.2, §10.1). The base-layer
	// upsert + Apply() handles .mcp.json regeneration, CLAUDE.md fragments,
	// notification fan-out and the workspaces.mcp_servers compat cache; we
	// just synthesize a MCPGenerateResult that mirrors what the legacy path
	// returns so wire callers (mobile/desktop/MCP shim) keep working.
	if s.sceneLayers != nil && s.sceneProj != nil {
		base, err := s.sceneLayers.EnsureBaseLayer(ctx, wsID)
		if err != nil {
			return nil, fmt.Errorf("ensure base layer: %w", err)
		}
		def, _ := DecodeDefinition(base.BaseDefinition)
		if def == nil {
			def = &SceneDefinition{}
		}
		mcp := make([]MCPDecl, 0, len(servers))
		for _, n := range servers {
			mcp = append(mcp, MCPDecl{Name: n})
		}
		def.MCP = mcp
		body, _ := json.Marshal(def)
		if err := s.sceneLayers.SaveBaseDefinition(ctx, wsID, string(body)); err != nil {
			return nil, fmt.Errorf("save base layer: %w", err)
		}
		// SceneProjector.Apply regenerates .mcp.json, CLAUDE.md fragments, and
		// mirrors the MCP list back into the compat column.
		if _, err := s.sceneProj.Apply(ctx, wsID); err != nil {
			return nil, fmt.Errorf("apply projection: %w", err)
		}
		// Return an empty MCPGenerateResult — wire callers (mobile/desktop/
		// MCP shim) only care that the call succeeded (200 OK); the result
		// envelope is reconstructed via subsequent GET /api/workspaces/:id/mcp.
		return &MCPGenerateResult{WrittenServers: servers}, nil
	}
	// Legacy path (no scene services wired — test fixtures or older builds).

	configDir := "" // multi-account removed: host ~/.claude/
	owner := OwnerRef{Type: ws.OwnerType, ID: ws.OwnerID}
	wsDir := owner.WorkspacePath(s.dataDir, ws.ID)
	if ws.CliType == "codex" {
		var res *MCPGenerateResult
		if s.mcpGen != nil {
			res, err = s.mcpGen.GenerateCodexConfigTomlWithExtras(wsDir, config.MCPGenerateOptions{
				WorkspaceID: ws.ID,
				InboxDir:    filepath.Join(wsDir, ".team", "inboxes"),
			}, servers, configDir)
			if err != nil {
				return nil, fmt.Errorf("write .codex/config.toml: %w", err)
			}
		} else {
			res = &MCPGenerateResult{}
		}
		body, _ := json.Marshal(servers)
		if err := s.q.UpdateWorkspaceMcpServers(ctx, store.UpdateWorkspaceMcpServersParams{
			McpServers: string(body),
			ID:         wsID,
		}); err != nil {
			return nil, fmt.Errorf("persist mcp_servers: %w", err)
		}
		return res, nil
	}
	mcpJSONPath := filepath.Join(wsDir, ".mcp.json")

	// 1. Snapshot for rollback. Missing file is fine — rollback path will
	//    just delete the new write if there was no prior file.
	var snapshot []byte
	hadSnapshot := false
	if b, rerr := os.ReadFile(mcpJSONPath); rerr == nil {
		snapshot = b
		hadSnapshot = true
	}

	// 2. Write new file via generator (stage-and-swap atomic rename).
	var res *MCPGenerateResult
	if s.mcpGen != nil {
		res, err = s.mcpGen.Generate(wsDir, config.MCPGenerateOptions{
			WorkspaceID: ws.ID,
		}, servers, configDir)
		if err != nil {
			return nil, fmt.Errorf("write .mcp.json: %w", err)
		}
	} else {
		res = &MCPGenerateResult{}
	}

	// 3. UPDATE DB.
	body, _ := json.Marshal(servers)
	if err := s.q.UpdateWorkspaceMcpServers(ctx, store.UpdateWorkspaceMcpServersParams{
		McpServers: string(body),
		ID:         wsID,
	}); err != nil {
		// 4. Best-effort rollback of the file write.
		if hadSnapshot {
			if werr := os.WriteFile(mcpJSONPath, snapshot, 0o644); werr != nil {
				slog.Error("UpdateMCPServers: DB UPDATE failed AND .mcp.json rollback failed",
					"workspace_id", wsID, "db_err", err, "file_err", werr)
			} else {
				slog.Warn("UpdateMCPServers: DB UPDATE failed; rolled back .mcp.json from snapshot",
					"workspace_id", wsID, "err", err)
			}
		} else {
			if werr := os.Remove(mcpJSONPath); werr != nil && !os.IsNotExist(werr) {
				slog.Error("UpdateMCPServers: DB UPDATE failed AND .mcp.json removal failed",
					"workspace_id", wsID, "db_err", err, "file_err", werr)
			} else {
				slog.Warn("UpdateMCPServers: DB UPDATE failed; removed newly-written .mcp.json (no snapshot)",
					"workspace_id", wsID, "err", err)
			}
		}
		return nil, fmt.Errorf("persist mcp_servers (file rolled back): %w", err)
	}
	return res, nil
}

// SetStrictMCP toggles whether the workspace agent ignores global MCP config
// (passes --strict-mcp-config at spawn, using only the workspace .mcp.json).
func (s *WorkspaceService) SetStrictMCP(ctx context.Context, wsID int64, strict bool) error {
	v := int64(0)
	if strict {
		v = 1
	}
	return s.q.UpdateWorkspaceStrictMCP(ctx, store.UpdateWorkspaceStrictMCPParams{
		StrictMcpConfig: v,
		ID:              wsID,
	})
}


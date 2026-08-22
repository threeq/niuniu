// Package service: SceneProjector — orchestrates Recompute → Apply for a
// workspace's scene stack.
//
// Recompute reads ListLayersForWorkspace, decodes each layer's definition
// (base via base_definition JSON, scene layers via the joined scene's
// definition column), and folds them in position-asc order into a Projection
// via the existing Projection.MergeFrom logic.
//
// Apply does the side-effects: regenerates .mcp.json, splices the
// CLAUDE.md fragment, imports asset rows, kicks off plugin installs, checks
// credential bindings, and persists the result into workspace_scene_projection.
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/notify"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// SceneProjector is the apply-time orchestrator. It does NOT own scene-layer
// CRUD (SceneLayerService does); it merely receives an already-mutated
// workspace, recomputes, and writes side-effects.
type SceneProjector struct {
	q             *store.Queries
	db            *store.DB
	dataDir       string
	mcpGen        *MCPConfigGenerator
	pluginInst    *PluginInstaller
	notifyHub     *notify.NotificationHub
	extCred       *ExternalCredentialService // optional, decrypts ${cred:...} placeholders
	localRunner   *LocalRunnerService        // optional, Epic #526 子B — prompt fragment injection

	// installCheck resolves whether a plugin is currently present on disk.
	// Defaults to pluginInst.IsInstalled; overridable in tests to exercise
	// ReconcileInstallStatus without a real plugin cache under the user's home.
	installCheck func(ctx context.Context, configDir string, p PluginDecl) (bool, error)
}

// SetLocalRunner wires the local-runner service so Apply can splice the
// configured claude.md prompt fragment into the worktree when the workspace's
// desktop runner is online (Epic #526 子B, #492). Optional; nil-safe.
func (p *SceneProjector) SetLocalRunner(s *LocalRunnerService) { p.localRunner = s }

// NewSceneProjector wires the dependencies. mcpGen and pluginInst may be nil
// in test scenarios — Apply degrades gracefully (skips the matching step
// rather than failing).
func NewSceneProjector(
	db *sql.DB,
	dataDir string,
	mcpGen *MCPConfigGenerator,
	pluginInst *PluginInstaller,
	notifyHub *notify.NotificationHub,
	extCred *ExternalCredentialService,
) *SceneProjector {
	return &SceneProjector{
		// store.NewQueries — driver-aware; see CLAUDE.md "Driver-aware DB access".
		q:             store.NewQueries(db),
		db:            store.Wrap(db),
		dataDir:       dataDir,
		mcpGen:        mcpGen,
		pluginInst:    pluginInst,
		notifyHub:     notifyHub,
		extCred:       extCred,
	}
}

// Recompute walks the layer stack and produces the merged Projection.
// Pure function of (DB state) — no side effects.
func (p *SceneProjector) Recompute(ctx context.Context, wsID int64) (*Projection, error) {
	layers, err := p.q.ListLayersForWorkspace(ctx, wsID)
	if err != nil {
		return nil, fmt.Errorf("list layers: %w", err)
	}
	// Batch-fetch every scene referenced by a non-base layer in one query so the
	// merge loop below is a map lookup instead of a per-layer GetScene.
	sceneIDs := make([]int64, 0, len(layers))
	for _, l := range layers {
		if l.IsBase != 1 && l.SceneID.Valid {
			sceneIDs = append(sceneIDs, l.SceneID.Int64)
		}
	}
	sceneByID := make(map[int64]store.Scene, len(sceneIDs))
	if len(sceneIDs) > 0 {
		scenes, err := p.q.GetScenesByIDs(ctx, sceneIDs)
		if err != nil {
			slog.Warn("scene projector: batch scene lookup failed", "workspace_id", wsID, "err", err)
		} else {
			for _, sc := range scenes {
				sceneByID[sc.ID] = sc
			}
		}
	}

	proj := NewProjection()
	for _, l := range layers {
		if l.IsBase == 1 {
			def, err := DecodeDefinition(l.BaseDefinition)
			if err != nil {
				slog.Warn("scene projector: bad base definition", "workspace_id", wsID, "err", err)
				continue
			}
			p.expandKnowledgeBases(ctx, wsID, def)
			proj.MergeFrom(def, BaseLayerOrigin)
			continue
		}
		if !l.SceneID.Valid {
			continue
		}
		scene, ok := sceneByID[l.SceneID.Int64]
		if !ok {
			slog.Warn("scene projector: scene lookup failed", "scene_id", l.SceneID.Int64)
			continue
		}
		def, err := DecodeDefinition(scene.Definition)
		if err != nil {
			slog.Warn("scene projector: bad scene definition", "scene_id", scene.ID, "err", err)
			continue
		}
		p.expandKnowledgeBases(ctx, wsID, def)
		proj.MergeFrom(def, LayerOrigin(scene.ID))
	}
	return proj, nil
}

// expandKnowledgeBases turns a scene's declared knowledge-base refs into real
// projection entries. A knowledge base is now a first-class resource with a
// source kind; an mcp-kind KB (an external KB MCP endpoint) is projected as an
// inline MCP server whose Authorization header is resolved from credstore at
// write time — exactly what the old hand-configured scene "kb" MCP server did,
// but driven by the unified KB list. local/url KBs are reached via the existing
// kb_search / workspace-mount path, so they add no MCP server here. Best-effort:
// an unresolvable or non-mcp KB is skipped, never aborting the projection.
func (p *SceneProjector) expandKnowledgeBases(ctx context.Context, wsID int64, def *SceneDefinition) {
	if len(def.KnowledgeBases) == 0 {
		return
	}
	ws, err := p.q.GetWorkspace(ctx, wsID)
	if err != nil {
		return
	}
	for _, ref := range def.KnowledgeBases {
		name := strings.TrimSpace(ref.Name)
		if name == "" {
			continue
		}
		kb, err := p.q.GetKnowledgeBaseByOwnerAndName(ctx, store.GetKnowledgeBaseByOwnerAndNameParams{
			OwnerType: ws.OwnerType, OwnerID: ws.OwnerID, Name: name,
		})
		if err != nil {
			continue // KB not found under this owner: skip
		}
		if kb.SourceKind != "mcp" {
			continue // local/url KBs are exposed via kb_search + workspace mount
		}
		alias := kbMcpAlias(kb)
		server := kbMcpServerName(kb)
		def.MCP = append(def.MCP, MCPDecl{
			Name: server,
			Config: map[string]any{
				"type": "http",
				"url":  kb.SourceAddr,
				"headers": map[string]any{
					"Authorization": "Bearer ${cred:" + alias + ".token}",
				},
			},
		})
		def.RequiredCredentials = append(def.RequiredCredentials, RequiredCredential{
			Alias: alias, Provider: "knowledge-base", Purpose: ref.Purpose,
		})
	}
}

// kbMcpAlias resolves the credstore alias an mcp-kind KB's token is stored under
// (source_config.cred_alias, set at create time), falling back to kb-<id>.
func kbMcpAlias(kb store.KnowledgeBase) string {
	if kb.SourceConfig != "" {
		var cfg struct {
			CredAlias string `json:"cred_alias"`
		}
		if json.Unmarshal([]byte(kb.SourceConfig), &cfg) == nil && cfg.CredAlias != "" {
			return cfg.CredAlias
		}
	}
	return fmt.Sprintf("kb-%d", kb.ID)
}

// kbMcpServerName derives a valid MCP server name from a KB: the KB name
// slugified (lower-case ascii, dash-joined), falling back to kb-<id> when the
// name has no ascii slug (e.g. all-CJK).
func kbMcpServerName(kb store.KnowledgeBase) string {
	s := strings.ToLower(strings.TrimSpace(kb.Name))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" || len(out) > 40 {
		return fmt.Sprintf("kb-%d", kb.ID)
	}
	return out
}

// Apply is the full pipeline: recompute → side-effects → persist. Failures
// in side-effects (file write, plugin install) are recorded into the
// ApplyResult rather than aborting the call, so the SPA can surface a
// "partial apply" banner with retry CTAs.
func (p *SceneProjector) Apply(ctx context.Context, wsID int64) (*ApplyResult, error) {
	ws, err := p.q.GetWorkspace(ctx, wsID)
	if err != nil {
		return nil, fmt.Errorf("load workspace: %w", err)
	}
	proj, err := p.Recompute(ctx, wsID)
	if err != nil {
		return nil, err
	}
	owner := OwnerRef{Type: ws.OwnerType, ID: ws.OwnerID}
	wsDir := owner.WorkspacePath(p.dataDir, ws.ID)
	configDir := p.resolveConfigDir(ws)

	// 0. Resolve ${cred:alias.field} placeholders in scene-authored MCP env,
	//    scoped to (owner, workspace-creator). userID comes from
	//    workspaces.created_by — for an org-shared mailbox this decrypts "the
	//    binding made by whoever created this workspace" (spec §4.3/§5). Servers
	//    with any unresolved placeholder are dropped entirely (never half-filled,
	//    spec §4.2.4) and their aliases surface in MissingCredentials below.
	userID := int64(0)
	if ws.CreatedBy.Valid {
		userID = ws.CreatedBy.Int64
	}
	resolvedMCPConfigs, droppedMCP, credMissing := p.resolveProjectionCredentials(ctx, owner, userID, proj)
	// 0b. office-mail / mcp-email-server is env-driven: inject the decrypted
	//     mailbox credential as MCP_EMAIL_SERVER_* env (IMAP always, SMTP only
	//     when write-permission is granted), and gate delete/move/mark via
	//     permissions.deny by the same write-permission (spec v2).
	emailMissing := p.applyEmailIntegration(ctx, owner, userID, ws.ID, wsDir, proj, resolvedMCPConfigs, droppedMCP)
	for a := range emailMissing {
		credMissing[a] = true
	}
	mcpNames := filterOutNames(proj.MCPNames, droppedMCP)

	// 1. Generate the active CLI MCP config. Use the same MCPGenerateOptions
	//    shape as workspace_mcp.go; the projection's MCPNames becomes extras.
	if p.mcpGen != nil {
		// harness/gate tools are git-bound. An office scene hides them by default
		// because the assistant is no-repo, but the user may manually bind a repo
		// to this workspace — then they clearly want git workflows, so keep the
		// harness group whenever a worktree actually exists. multi-agent stays
		// hidden regardless (the assistant is single-agent with or without a repo).
		disableGroups := proj.DisableToolGroups
		if wts, werr := p.q.ListWorktrees(ctx, ws.ID); werr == nil && len(wts) > 0 {
			disableGroups = dropToolGroup(disableGroups, sceneToolGroupHarness)
		}
		opts := config.MCPGenerateOptions{
			WorkspaceID:       ws.ID,
			InboxDir:          filepath.Join(wsDir, ".team", "inboxes"),
			ExtraMCPServers:   resolvedMCPConfigs,
			DisableToolGroups: disableGroups,
			EnableToolGroups:  proj.EnableToolGroups,
		}
		if ws.CliType == "codex" {
			if _, err := p.mcpGen.GenerateCodexConfigTomlWithExtras(wsDir, opts, mcpNames, configDir); err != nil {
				slog.Warn("scene projector: .codex/config.toml generate failed", "workspace_id", wsID, "err", err)
			}
		} else if _, err := p.mcpGen.Generate(wsDir, opts, mcpNames, configDir); err != nil {
			slog.Warn("scene projector: .mcp.json generate failed", "workspace_id", wsID, "err", err)
		}
	}

	// 1b. Local-runner prompt fragment (Epic #526 子B, #492): when the bound
	//     desktop runner is online, splice the user-configured claude.md snippet
	//     into the worktree prompt so the agent knows to prefer local_exec etc.
	//     Withdrawn automatically on the next projection once the runner drops.
	if p.localRunner != nil {
		if frag := p.localRunner.PromptFragmentFor(ctx, ws.ID); frag != "" {
			proj.Prompts = append(proj.Prompts, PromptFragment{
				ID:    "local-runner",
				Title: "Local Runner",
				Body:  frag,
			})
		}
	}

	// 2. Agent instruction splice. Claude reads CLAUDE.md; Codex reads
	// AGENTS.md. For codex workspaces we also keep CLAUDE.md in sync because
	// some repository worktrees still carry Claude-oriented local instructions.
	if err := p.writeScenePromptFiles(wsDir, ws.CliType, proj); err != nil {
		slog.Warn("scene projector: prompt file write failed", "workspace_id", wsID, "err", err)
	}

	// 3. Asset imports.
	//    project_templates is skipped because its slug uniqueness is
	//    per-project, not per-owner — we have no project context here.
	p.importAssets(ctx, ws, proj)

	// 3b. Materialize scene-referenced agents into the workspace's Claude
	//     subagent dir (<wsDir>/.claude/agents). Scenes reference existing
	//     agents by name; nothing is written to the global agent store.
	p.materializeWorkspaceAgents(ctx, ws, wsDir, proj)

	// 3c. Materialize scene-declared vendored skills into the workspace's Claude
	//     skills dir (<wsDir>/.claude/skills). Pure local file copies (no install
	//     CLI), so — like agents — they apply automatically on scene-enable.
	p.materializeWorkspaceSkills(ws, wsDir, proj)

	// 4. Auto-install scene-declared plugins — the scene's own skills (e.g.
	//    document-skills@anthropic-agent-skills) plus any other plugins it
	//    brings. Enabling a scene IS the user's explicit action, so the plugins
	//    it declares are installed here automatically; there is no separate
	//    "Install" click in the SPA. `claude plugin install` is idempotent and
	//    pre-flighted by a local installed check, so already-installed plugins
	//    resolve to "skipped" on later Applies without network/CLI work, and a
	//    per-install timeout keeps a hung marketplace from blocking Apply. The
	//    results (installed / skipped / failed) are persisted below; the SPA
	//    banner surfaces only genuine failures — once everything is safely
	//    installed there is nothing left for the banner to show.
	var installPlan []PluginInstallResult
	if p.pluginInst != nil && len(proj.Plugins) > 0 {
		if ws.CliType == "codex" {
			// Codex CLI 0.x has no `codex plugin install` — codex plugins are
			// MCP servers declared in .codex/config.toml [mcp_servers.*], which
			// niuniu already projects from scene.MCPNames above. Surface each
			// claude-marketplace plugin as 'unsupported' so the SPA can show an
			// explanatory banner without retry CTAs.
			installPlan = make([]PluginInstallResult, 0, len(proj.Plugins))
			for _, pl := range proj.Plugins {
				installPlan = append(installPlan, PluginInstallResult{
					Source: pl.Source,
					Ref:    pl.Ref,
					Status: PluginInstallStatusUnsupported,
					Stderr: "codex workspaces do not use the claude marketplace; configure plugins as MCP servers in the scene's MCP list",
				})
			}
		} else {
			installPlan = p.pluginInst.ApplyForCLI(ctx, "claude", configDir, proj.Plugins)
			// Model A: enable the scene's plugins at the workspace project level so a
			// globally-installed-but-disabled plugin turns on only for this workspace.
			if p.mcpGen != nil {
				ids := make([]string, 0, len(proj.Plugins))
				for _, pl := range proj.Plugins {
					ids = append(ids, pl.Source)
				}
				if err := p.mcpGen.SetWorkspaceEnabledPlugins(wsDir, ids); err != nil {
					slog.Warn("scene projector: write enabledPlugins failed", "workspace_id", wsID, "err", err)
				}
			}
		}
	}

	// 5. Credential gap analysis. Scoped to (owner, workspace-creator) so an
	//    org member who hasn't bound the shared mailbox still sees it as missing
	//    even if a different member has (spec §4.3). Aliases whose placeholders
	//    failed to resolve at injection time (cred exists but a field is absent)
	//    are unioned in so the "bind credential" card surfaces them too.
	missing := p.findMissingCredentials(ctx, owner, userID, proj.RequiredCredentials)
	missing = mergeInjectionMissing(missing, credMissing, proj.RequiredCredentials)

	// 6. Restart-required heuristic: if the prior projection's process-relevant
	//    digest differs from the new one, the agent must be restarted to pick
	//    up the new MCP/plugin set.
	digest := proj.Digest()
	prev, _ := p.q.GetProjection(ctx, wsID)
	restartRequired := prev.Digest != "" && prev.Digest != digest

	// 6b. Drop rows the user has dismissed for this workspace so the banner
	//     stops surfacing plugins they explicitly chose to ignore. The
	//     dismissed list is preserved across recomputes (UpsertProjection
	//     never touches the dismissed_plugins column).
	dismissed := decodeDismissedPlugins(prev.DismissedPlugins)
	installPlan = filterDismissedResults(installPlan, dismissed)

	// 7. Persist projection cache.
	body, _ := json.Marshal(proj)
	missingJSON, _ := json.Marshal(missing)
	// Note: the column is historically named install_failures, but now also
	// carries pending and skipped entries so the SPA has a complete picture
	// of what install actions are available. Status enum is the
	// authoritative discriminator (installed / skipped / pending / failed).
	failuresJSON := InstallResultsToJSON(installPlan)
	restartFlag := int64(0)
	if restartRequired {
		restartFlag = 1
	}
	if err := p.q.UpsertProjection(ctx, store.UpsertProjectionParams{
		WorkspaceID:         wsID,
		Digest:              digest,
		ProjectedDefinition: string(body),
		MissingCredentials:  string(missingJSON),
		InstallFailures:     failuresJSON,
		RestartRequired:     restartFlag,
	}); err != nil {
		slog.Warn("scene projector: persist projection failed", "workspace_id", wsID, "err", err)
	}

	// 8. Mirror MCP names into workspaces.mcp_servers compat column so the
	//    pre-scene UI (spec §6.5) keeps reading the same list. Failure is
	//    non-fatal — the next direct UpdateMCPServers will re-converge.
	mcpJSON, _ := json.Marshal(proj.MCPNames)
	if err := p.q.UpdateWorkspaceMcpServers(ctx, store.UpdateWorkspaceMcpServersParams{
		McpServers: string(mcpJSON),
		ID:         wsID,
	}); err != nil {
		slog.Warn("scene projector: mirror mcp_servers failed", "workspace_id", wsID, "err", err)
	}

	// 9. Notify SPA.
	if p.notifyHub != nil {
		p.notifyHub.Broadcast(notify.Notification{
			Topic:     SceneProjectionTopic,
			Action:    "updated",
			ID:        wsID,
			OwnerType: ws.OwnerType,
			OwnerID:   ws.OwnerID,
			Extra: map[string]any{
				"workspace_id":     wsID,
				"restart_required": restartRequired,
				"missing_count":    len(missing),
				"failure_count":    countByStatus(installPlan, PluginInstallStatusFailed),
				"pending_count":    countByStatus(installPlan, PluginInstallStatusPending),
			},
		})
	}

	return &ApplyResult{
		Projection:         proj,
		MissingCredentials: missing,
		InstallFailures:    installPlan,
		RestartRequired:    restartRequired,
		Digest:             digest,
		DismissedPlugins:   dismissed,
	}, nil
}

// InstallPlugins is the user-initiated counterpart to Apply's Plan step.
// Called by the /api/workspaces/:id/scene/plugins/install handler when the
// user clicks "Install" in the SPA. Re-uses the same projection (Recompute)
// to make sure we install the *current* plugin set, not a stale one. If
// `sources` is non-empty, only plugins whose `source` matches are installed
// (per-row install button); empty means "install all pending".
//
// Returns the updated install result list — installed / skipped / failed
// for each row that was attempted. The projection cache is refreshed with
// the new statuses so the SPA banner reflects the outcome.
func (p *SceneProjector) InstallPlugins(ctx context.Context, wsID int64, sources []string) ([]PluginInstallResult, error) {
	if p.pluginInst == nil {
		return nil, fmt.Errorf("plugin installer not wired")
	}
	ws, err := p.q.GetWorkspace(ctx, wsID)
	if err != nil {
		return nil, fmt.Errorf("load workspace: %w", err)
	}
	proj, err := p.Recompute(ctx, wsID)
	if err != nil {
		return nil, err
	}
	configDir := p.resolveConfigDir(ws)

	// Filter to the requested sources (empty = all current plugin decls).
	want := proj.Plugins
	if len(sources) > 0 {
		set := make(map[string]struct{}, len(sources))
		for _, s := range sources {
			set[s] = struct{}{}
		}
		want = want[:0]
		for _, decl := range proj.Plugins {
			if _, ok := set[decl.Source]; ok {
				want = append(want, decl)
			}
		}
	}

	results := p.pluginInst.Apply(ctx, configDir, want)

	// Merge results back into the cached install_failures shape:
	// re-plan the full list (so unchanged rows keep their pending/skipped
	// status), then overwrite the affected rows.
	full := p.pluginInst.Plan(ctx, configDir, proj.Plugins)
	bySource := make(map[string]PluginInstallResult, len(results))
	for _, r := range results {
		bySource[r.Source] = r
	}
	for i, r := range full {
		if upd, ok := bySource[r.Source]; ok {
			full[i] = upd
		}
	}
	// Honor existing dismissals so an install attempt on a non-dismissed
	// plugin doesn't resurrect dismissed rows into the banner.
	prev, _ := p.q.GetProjection(ctx, wsID)
	full = filterDismissedResults(full, decodeDismissedPlugins(prev.DismissedPlugins))
	failuresJSON := InstallResultsToJSON(full)
	if err := p.q.UpsertProjection(ctx, store.UpsertProjectionParams{
		WorkspaceID:         wsID,
		Digest:              proj.Digest(),
		ProjectedDefinition: mustMarshal(proj),
		MissingCredentials:  mustMarshal(p.findMissingCredentials(ctx, OwnerRef{Type: ws.OwnerType, ID: ws.OwnerID}, nullInt64Value(ws.CreatedBy), proj.RequiredCredentials)),
		InstallFailures:     failuresJSON,
		RestartRequired:     0,
	}); err != nil {
		slog.Warn("install plugins: persist projection failed", "workspace_id", wsID, "err", err)
	}
	if p.notifyHub != nil {
		p.notifyHub.Broadcast(notify.Notification{
			Topic:     SceneProjectionTopic,
			Action:    "plugins_installed",
			ID:        wsID,
			OwnerType: ws.OwnerType,
			OwnerID:   ws.OwnerID,
			Extra: map[string]any{
				"workspace_id":    wsID,
				"attempted_count": len(results),
			},
		})
	}
	return results, nil
}

// SetPluginDismissed adds (dismissed=true) or removes (dismissed=false) a
// plugin source from the workspace's dismissed list, then re-runs Apply so the
// banner reflects the change immediately. This is the user's escape hatch when
// a scene declares a plugin they cannot or do not want to install (e.g. a wrong
// marketplace in the source) — without it the pending/failed banner is sticky.
//
// The dismissed list is stored in its own column and is never overwritten by
// Apply's UpsertProjection, so dismissals survive recompute. The list is not
// constrained to currently-declared sources: dismissing a plugin that later
// disappears from the scene is harmless (the filter is a membership test).
func (p *SceneProjector) SetPluginDismissed(ctx context.Context, wsID int64, source string, dismissed bool) (*ApplyResult, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil, fmt.Errorf("source is required")
	}
	row, err := p.q.GetProjection(ctx, wsID)
	if err != nil {
		// No projection row yet — run Apply once to create it, then retry. This
		// keeps SetDismissedPlugins' UPDATE (which needs an existing row) valid.
		if _, aerr := p.Apply(ctx, wsID); aerr != nil {
			return nil, fmt.Errorf("ensure projection: %w", aerr)
		}
		row, err = p.q.GetProjection(ctx, wsID)
		if err != nil {
			return nil, fmt.Errorf("load projection: %w", err)
		}
	}
	set := map[string]struct{}{}
	for _, s := range decodeDismissedPlugins(row.DismissedPlugins) {
		set[s] = struct{}{}
	}
	if dismissed {
		set[source] = struct{}{}
	} else {
		delete(set, source)
	}
	list := make([]string, 0, len(set))
	for s := range set {
		list = append(list, s)
	}
	sort.Strings(list)
	encoded, _ := json.Marshal(list)
	if err := p.q.SetDismissedPlugins(ctx, store.SetDismissedPluginsParams{
		DismissedPlugins: string(encoded),
		WorkspaceID:      wsID,
	}); err != nil {
		return nil, fmt.Errorf("persist dismissed plugins: %w", err)
	}
	// Re-run Apply so install_failures is re-derived with the new dismissed set.
	return p.Apply(ctx, wsID)
}

// decodeDismissedPlugins parses the stored JSON array of dismissed plugin
// sources, tolerating empty / malformed values (treated as none dismissed).
func decodeDismissedPlugins(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// filterDismissedResults removes plugin result rows whose source is in the
// dismissed set. Returns the input unchanged when nothing is dismissed.
func filterDismissedResults(in []PluginInstallResult, dismissed []string) []PluginInstallResult {
	if len(dismissed) == 0 || len(in) == 0 {
		return in
	}
	set := make(map[string]struct{}, len(dismissed))
	for _, s := range dismissed {
		set[s] = struct{}{}
	}
	out := make([]PluginInstallResult, 0, len(in))
	for _, r := range in {
		if _, ok := set[r.Source]; ok {
			continue
		}
		out = append(out, r)
	}
	return out
}

// ReconcileInstallStatus re-checks the cached plugin install statuses against
// the on-disk installed state (a local stat — no install is spawned) and
// returns the reconciled list plus whether anything changed. Rows whose plugin
// is now present on disk are flipped to "skipped" and their stale stderr
// dropped. Once every scene-declared skill is safely installed, the SPA has
// no pending/failed row left and the projection banner disappears.
//
// Called by GET /scene-projection so reopening a workspace converges the
// banner against reality: an earlier failed auto-install may have succeeded on
// a later Apply, or the user may have installed the plugin by hand in a
// terminal (the SkillsGate-style workflow). Codex workspaces are skipped — their
// plugins are never installed via the CLI, so there is nothing to reconcile.
func (p *SceneProjector) ReconcileInstallStatus(ctx context.Context, wsID int64) ([]PluginInstallResult, bool, error) {
	ws, err := p.q.GetWorkspace(ctx, wsID)
	if err != nil {
		return nil, false, fmt.Errorf("load workspace: %w", err)
	}
	row, err := p.q.GetProjection(ctx, wsID)
	if err != nil {
		// No cached projection row — nothing to reconcile.
		return nil, false, nil
	}
	failures := DecodeInstallResults(row.InstallFailures)
	if len(failures) == 0 || ws.CliType == "codex" {
		return failures, false, nil
	}
	configDir := p.resolveConfigDir(ws)
	changed := false
	for i := range failures {
		r := &failures[i]
		if r.Status == PluginInstallStatusInstalled || r.Status == PluginInstallStatusSkipped {
			continue
		}
		installed, err := p.pluginInstalled(ctx, configDir, PluginDecl{Source: r.Source, Ref: r.Ref})
		if err != nil || !installed {
			continue
		}
		r.Status = PluginInstallStatusSkipped
		r.Stderr = ""
		changed = true
	}
	if changed {
		if err := p.q.UpsertProjection(ctx, store.UpsertProjectionParams{
			WorkspaceID:         wsID,
			Digest:              row.Digest,
			ProjectedDefinition: row.ProjectedDefinition,
			MissingCredentials:  row.MissingCredentials,
			InstallFailures:     InstallResultsToJSON(failures),
			RestartRequired:     row.RestartRequired,
		}); err != nil {
			slog.Warn("scene projector: reconcile install status persist failed", "workspace_id", wsID, "err", err)
		}
	}
	return failures, changed, nil
}

// pluginInstalled reports whether a plugin is present on disk for the given
// configDir, honoring the installCheck test seam when set and falling back to
// pluginInst.IsInstalled (a local stat). A nil installer means "not installed".
func (p *SceneProjector) pluginInstalled(ctx context.Context, configDir string, decl PluginDecl) (bool, error) {
	if p.installCheck != nil {
		return p.installCheck(ctx, configDir, decl)
	}
	if p.pluginInst == nil {
		return false, nil
	}
	return p.pluginInst.IsInstalled(ctx, configDir, decl)
}

func mustMarshal(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func countByStatus(rs []PluginInstallResult, s PluginInstallStatus) int {
	n := 0
	for _, r := range rs {
		if r.Status == s {
			n++
		}
	}
	return n
}

// SceneProjectionTopic is the notify topic emitted after each Apply. SPA
// subscribes via the notification dispatcher and invalidates the scene
// projection query.
const SceneProjectionTopic = "scene_projection_updated"

// resolveConfigDir returns the Claude config dir for a workspace's projection.
// Multi-account switching removed: always the host's global ~/.claude/ ("").
func (p *SceneProjector) resolveConfigDir(ws store.Workspace) string {
	_ = ws
	return ""
}

func (p *SceneProjector) writeScenePromptFiles(wsDir, cliType string, proj *Projection) error {
	if err := p.writeScenePromptFile(filepath.Join(wsDir, "CLAUDE.md"), proj); err != nil {
		return err
	}
	if cliType == "codex" {
		if err := p.writeScenePromptFile(filepath.Join(wsDir, "AGENTS.md"), proj); err != nil {
			return err
		}
	}
	return nil
}

// writeScenePromptFile reads an agent instruction file, swaps the
// niuniu-managed scene block for the projection's rendered fragment (or
// removes the block when there are no prompts), and writes the result
// atomically.
func (p *SceneProjector) writeScenePromptFile(mdPath string, proj *Projection) error {
	var existing string
	if b, err := os.ReadFile(mdPath); err == nil {
		existing = string(b)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read prompt file %s: %w", filepath.Base(mdPath), err)
	}
	block := proj.RenderCLAUDEMdFragment()
	merged := ReplaceOrAppendCLAUDEMdBlock(existing, block)
	if merged == existing {
		return nil
	}
	tmp := mdPath + ".tmp-niuniu-scene"
	if err := os.WriteFile(tmp, []byte(merged), 0o644); err != nil {
		return fmt.Errorf("write tmp prompt file %s: %w", filepath.Base(mdPath), err)
	}
	if err := os.Rename(tmp, mdPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename prompt file %s: %w", filepath.Base(mdPath), err)
	}
	return nil
}

// importAssets persists the projection's scene assets if
// they don't already exist for the owner. Each newly-created asset is
// recorded in scene_asset_imports so a later Detach can clean them up.
// (Cleanup is out of M1 scope; the rows act as provenance markers only.)
func (p *SceneProjector) importAssets(ctx context.Context, ws store.Workspace, proj *Projection) {
	owner := OwnerRef{Type: ws.OwnerType, ID: ws.OwnerID}

	for _, ep := range proj.Assets.EnvPresets {
		id, ok := p.upsertEnvPresetAsset(ctx, owner, ep)
		if !ok {
			continue
		}
		p.recordImport(ctx, ws.ID, sceneIDForAsset(proj, "env_preset", ep.Slug), "env_preset", id)
	}
	// NOTE: quick_actions are intentionally NOT imported into the quick_actions
	// table. Scene-provided quick actions are surfaced live (parsed from the
	// projection) by GET /api/workspaces/:id/quick-actions as a separate group,
	// so persisting them here would duplicate them and blur the boundary with
	// the user's own personal/org quick actions.
	for _, hs := range proj.Assets.HarnessSpecs {
		id, ok := p.upsertHarnessSpecAsset(ctx, owner, hs)
		if !ok {
			continue
		}
		p.recordImport(ctx, ws.ID, sceneIDForAsset(proj, "harness_spec", hs.Slug), "harness_spec", id)
	}
	// NOTE: agents are NOT imported into the agents table. Scenes only
	// REFERENCE existing agents (managed on the Agents page) by name; the
	// referenced agent markdown is materialized into the workspace's Claude
	// agent dir by materializeWorkspaceAgents (see Apply step 3b).
	// project_templates: skipped because idempotency is project-scoped and
	// the projector only has workspace/owner context.
}

// materializeWorkspaceAgents copies each scene-referenced agent into the
// workspace's CLI-specific subagent directory, resolved from ws.CliType via
// workspaceAgentTargetFor (claude → .claude/agents/<name>.md, qwen →
// .qwen/agents/<name>.md, codex → .codex/agents/<name>.toml). Each file is
// stamped `managed_by` so it is cleaned up on the next recompute. To survive a
// CLI switch, ALL CLIs' managed agents are cleared first (user agents are
// preserved); only the active CLI's directory is then populated.
func (p *SceneProjector) materializeWorkspaceAgents(ctx context.Context, ws store.Workspace, wsDir string, proj *Projection) {
	// Always clear stale niuniu-managed agents across every CLI directory so
	// detached scenes — and a changed CliType — leave no residue behind.
	for _, ct := range agentCliTypes {
		cleanManagedAgentsDir(filepath.Join(wsDir, workspaceAgentTargetFor(ct).dir))
	}

	if len(proj.Assets.Agents) == 0 {
		return
	}
	target := workspaceAgentTargetFor(ws.CliType)
	agentsDir := filepath.Join(wsDir, target.dir)
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		slog.Warn("materialize agents: mkdir failed", "workspace_id", ws.ID, "err", err)
		return
	}
	owner := OwnerRef{Type: ws.OwnerType, ID: ws.OwnerID}
	for _, ref := range proj.Assets.Agents {
		name := strings.TrimSpace(ref.Name)
		if name == "" {
			continue
		}
		var dirPath, description string
		err := p.db.QueryRowContext(ctx,
			`SELECT dir_path, description FROM agents WHERE owner_type = ? AND owner_id = ? AND name = ?`,
			owner.Type, owner.ID, name).Scan(&dirPath, &description)
		if err != nil {
			slog.Warn("materialize agents: referenced agent not found for owner", "workspace_id", ws.ID, "name", name, "err", err)
			continue
		}
		content, err := os.ReadFile(dirPath)
		if err != nil {
			slog.Warn("materialize agents: read agent file failed", "name", name, "err", err)
			continue
		}
		managed := target.render(string(content), name, description)
		if err := os.WriteFile(filepath.Join(agentsDir, name+target.ext), []byte(managed), 0o644); err != nil {
			slog.Warn("materialize agents: write failed", "name", name, "err", err)
		}
	}
}

// sceneIDForAsset picks the first scene-id origin from the projection's
// provenance map. The base layer (origin == -1) is filtered out because
// scene_asset_imports requires a real scene_id FK.
func sceneIDForAsset(proj *Projection, kind, slug string) int64 {
	for _, o := range proj.Provenance[kind+":"+slug] {
		if int64(o) > 0 {
			return int64(o)
		}
	}
	return 0
}

func (p *SceneProjector) upsertEnvPresetAsset(ctx context.Context, owner OwnerRef, ep EnvPresetAsset) (int64, bool) {
	// Existing row by owner+slug?
	var existingID int64
	err := p.db.QueryRowContext(ctx,
		`SELECT id FROM env_presets WHERE owner_type = ? AND owner_id = ? AND slug = ?`,
		owner.Type, owner.ID, ep.Slug).Scan(&existingID)
	if err == nil {
		return existingID, false // already present, no import to record
	}
	if !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("import env_preset: lookup failed", "slug", ep.Slug, "err", err)
		return 0, false
	}
	envJSON, _ := json.Marshal(ep.Env)
	name := ep.Name
	if name == "" {
		name = ep.Slug
	}
	// env_presets.name is globally UNIQUE — disambiguate by suffixing
	// owner_id when there's a collision. Best-effort; on persistent failure
	// we give up rather than thrash.
	for attempt := 0; attempt < 4; attempt++ {
		try := name
		if attempt > 0 {
			try = fmt.Sprintf("%s (%s-%d-%d)", name, owner.Type, owner.ID, attempt)
		}
		res, err := p.db.ExecContext(ctx,
			`INSERT INTO env_presets (name, description, env, owner_type, owner_id, slug)
			   VALUES (?, ?, ?, ?, ?, ?)`,
			try, ep.Description, string(envJSON), owner.Type, owner.ID, ep.Slug)
		if err == nil {
			id, _ := res.LastInsertId()
			return id, true
		}
		if !isUniqueViolationErr(err) {
			slog.Warn("import env_preset: insert failed", "slug", ep.Slug, "err", err)
			return 0, false
		}
	}
	return 0, false
}

func (p *SceneProjector) upsertHarnessSpecAsset(ctx context.Context, owner OwnerRef, hs HarnessSpecAsset) (int64, bool) {
	name := hs.Name
	if name == "" {
		name = hs.Slug
	}
	input := harnessSpecAssetInput(hs)
	category := stringFromMap(hs.Payload, "category", input.Category, "quality")
	_ = owner // harness_specs is a single global library (no owner)

	var existingID int64
	err := p.db.QueryRowContext(ctx,
		`SELECT id FROM harness_specs WHERE category = ? AND name = ?`,
		category, name).Scan(&existingID)
	if err == nil {
		return existingID, false
	}
	if !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("import harness_spec: lookup failed", "slug", hs.Slug, "err", err)
		return 0, false
	}

	var id int64
	err = p.db.QueryRowContext(ctx,
		`INSERT INTO harness_specs (
		    category, name, enabled, severity, config,
		    kind, target, pattern, pattern_flags, command, timeout_sec,
		    expected_exit_code, extract_regex, threshold_value, threshold_op,
		    file_paths, trigger_on, judge_prompt, judge_model
		  )
		  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		  RETURNING id`,
		category, name, input.Enabled, input.Severity, input.Config,
		input.Kind, input.Target, input.Pattern, input.PatternFlags, input.Command, input.TimeoutSec,
		input.ExpectedExitCode, input.ExtractRegex, input.ThresholdValue, input.ThresholdOp,
		input.FilePaths, input.TriggerOn, input.JudgePrompt, input.JudgeModel).Scan(&id)
	if err != nil {
		slog.Warn("import harness_spec: insert failed", "slug", hs.Slug, "err", err)
		return 0, false
	}
	return id, true
}

type sceneHarnessSpecInput struct {
	Scope            string  `json:"scope"`
	Category         string  `json:"category"`
	Enabled          int64   `json:"enabled"`
	Severity         string  `json:"severity"`
	Config           string  `json:"config"`
	Kind             string  `json:"kind"`
	Target           string  `json:"target"`
	Pattern          string  `json:"pattern"`
	PatternFlags     string  `json:"pattern_flags"`
	Command          string  `json:"command"`
	TimeoutSec       int64   `json:"timeout_sec"`
	ExpectedExitCode int64   `json:"expected_exit_code"`
	ExtractRegex     string  `json:"extract_regex"`
	ThresholdValue   float64 `json:"threshold_value"`
	ThresholdOp      string  `json:"threshold_op"`
	FilePaths        string  `json:"file_paths"`
	TriggerOn        string  `json:"trigger_on"`
	JudgePrompt      string  `json:"judge_prompt"`
	JudgeModel       string  `json:"judge_model"`
}

func harnessSpecAssetInput(hs HarnessSpecAsset) sceneHarnessSpecInput {
	var in sceneHarnessSpecInput
	if len(hs.Payload) > 0 {
		b, _ := json.Marshal(hs.Payload)
		_ = json.Unmarshal(b, &in)
	}
	if in.Scope == "" {
		in.Scope = "global"
	}
	if in.Category == "" {
		in.Category = "quality"
	}
	if in.Enabled == 0 {
		in.Enabled = 1
	}
	if in.Severity == "" {
		in.Severity = "warning"
	}
	if in.Config == "" {
		in.Config = "{}"
	}
	if in.Kind == "" {
		in.Kind = "regex_match"
	}
	if in.TimeoutSec == 0 {
		in.TimeoutSec = 120
	}
	if in.FilePaths == "" {
		in.FilePaths = "[]"
	}
	if in.TriggerOn == "" {
		in.TriggerOn = "phase_exit"
	}
	if in.JudgeModel == "" {
		in.JudgeModel = "claude-haiku-4-5-20251001"
	}
	return in
}

func stringFromMap(m map[string]any, key, current, fallback string) string {
	if current != "" {
		return current
	}
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return fallback
}

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func (p *SceneProjector) recordImport(ctx context.Context, wsID, sceneID int64, kind string, assetID int64) {
	if sceneID == 0 {
		return // imported from base layer; no scene to attribute to
	}
	if err := p.q.CreateImport(ctx, store.CreateImportParams{
		WorkspaceID: wsID,
		SceneID:     sceneID,
		AssetKind:   kind,
		AssetID:     assetID,
	}); err != nil {
		slog.Warn("record scene asset import: failed", "ws", wsID, "scene", sceneID, "kind", kind, "asset", assetID, "err", err)
	}
}

// findMissingCredentials checks each required-cred alias against the owner's
// external_provider_credentials. Implemented inline (no sqlc helper exists
// for this lookup shape) per architecture review.
//
// The user_id dimension matches the injection scope (spec §4.3): existence is
// checked for the same (owner, user) tuple the projector decrypts from, so an
// org where member A bound an imap credential never makes member B's workspace
// read as "already satisfied".
func (p *SceneProjector) findMissingCredentials(ctx context.Context, owner OwnerRef, userID int64, required []RequiredCredential) []MissingCredential {
	if len(required) == 0 {
		return nil
	}
	rows, err := p.db.QueryContext(ctx,
		`SELECT DISTINCT provider FROM external_provider_credentials WHERE owner_type = ? AND owner_id = ? AND user_id = ?`,
		owner.Type, owner.ID, userID)
	if err != nil {
		slog.Warn("find missing creds: query failed", "err", err)
		// Treat all as missing on query failure so the user gets *some* signal.
		out := make([]MissingCredential, len(required))
		for i, c := range required {
			out[i] = MissingCredential(c)
		}
		return out
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var prov string
		if err := rows.Scan(&prov); err == nil {
			have[prov] = true
		}
	}
	var missing []MissingCredential
	for _, c := range required {
		if !have[c.Provider] {
			missing = append(missing, MissingCredential(c))
		}
	}
	return missing
}

// isUniqueViolationErr matches both SQLite ("UNIQUE constraint failed") and
// PG ("duplicate key value violates unique constraint") shapes.
func isUniqueViolationErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "UNIQUE constraint failed") ||
		strings.Contains(s, "duplicate key value")
}

// sceneToolGroupHarness mirrors cmd/niuniu-mcp's toolGroupHarness — the
// git/repo-bound tool group (gate_run / gate_results / harness_pre_commit_check)
// that a scene may hide by default but which is re-enabled when the workspace
// has a bound repo. Keep this string in sync with the niuniu-mcp constant.
const sceneToolGroupHarness = "harness"

// dropToolGroup returns groups without the named group (order preserved). Used
// to re-enable a scene-disabled, repo-bound tool group at projection time.
func dropToolGroup(groups []string, drop string) []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		if g != drop {
			out = append(out, g)
		}
	}
	return out
}

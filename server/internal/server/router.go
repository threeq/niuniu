package server

import (
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	apipkg "github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/auth"
	"github.com/niuniu-dev/niuniu/license"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

func (s *Server) setupRoutes() {
	// CORS — AllowOriginFunc (not AllowAllOrigins) to work with AllowCredentials
	s.engine.Use(cors.New(cors.Config{
		AllowOriginFunc: func(origin string) bool { return true },
		AllowMethods:    []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:    []string{"Origin", "Content-Type", "Authorization"},
		ExposeHeaders:   []string{"X-API-Version", "X-Min-Client-Version"},
		MaxAge:          12 * time.Hour,
	}))

	// Health check — no auth required
	s.engine.GET("/api/health", s.healthHandler.Health)

	// IM Bot optional public webhook (Epic #555, design §5.2) — no auth: external
	// IM platforms POST events here for publicly-deployed channels. Stream/LAN
	// channels never use it. The equivalent inbound entry to the stream long
	// connection; the pairing-approval gate is the security boundary.
	s.engine.POST("/api/imbot/webhook/:channelId", s.imbotHandler.Webhook)

	// AI-onboarding credential submission (Epic #555 T3): one-time token is the
	// sole authorization — mounted alongside the public webhook, OUTSIDE the
	// authenticated api group. No secret values are ever echoed in the response.
	s.engine.POST("/api/imbot/onboarding/:token/credential", s.imbotHandler.SubmitOnboardingCredential)
	// AI-onboarding token info (read-only): returns platform/channel_name/connection_mode
	// so the credential form can render platform-correct fields. Does NOT consume the token.
	s.engine.GET("/api/imbot/onboarding/:token/info", s.imbotHandler.GetOnboardingInfo)
	// WeChat 微信ClawBot QR-scan connect (server-driven, token-gated): start the
	// QR handshake and long-poll its status. On confirmation the poll redeems the
	// onboarding token and creates the channel (like the credential form).
	s.engine.POST("/api/imbot/onboarding/:token/wechat/login/start", s.imbotHandler.WechatLoginStart)
	s.engine.POST("/api/imbot/onboarding/:token/wechat/login/poll", s.imbotHandler.WechatLoginPoll)

	// Latest desktop release (proxied from the official website changelog) —
	// no auth required; only consumed by the personal-mode "check for updates".
	s.engine.GET("/api/app-update/latest", s.appUpdateHandler.Latest)

	// Auth routes — no auth required, rate limited
	authGroup := s.engine.Group("/api/auth")
	{
		authGroup.POST("/login",
			auth.RateLimitMiddleware(0.1, 5),
			s.authHandler.Login)
		authGroup.POST("/refresh",
			auth.RateLimitMiddleware(1, 10),
			s.authHandler.Refresh)
		authGroup.POST("/mfa/verify",
			auth.RateLimitMiddleware(0.1, 5),
			s.mfaHandler.Verify)
	}

	api := s.engine.Group("/api")
	api.Use(auth.IdentityResolver(s.cfg, s.db))
	api.Use(auth.Middleware(s.cfg.Auth.Enabled, s.authSecret))
	// License gate: block writes/run-class actions when the license is locked.
	api.Use(apipkg.LicenseGuard(func() bool {
		return s.licenseSvc.ReadOnly()
	}))

	// Consent gate: block writes/run-class actions from a caller who has not yet
	// accepted the current privacy & disclaimer agreement. Returns false (allow)
	// when no user is resolved, so unauthenticated requests are owned by the auth
	// middleware. Shared by the /api group, the /mcp group (AI execution
	// interface), and the WS run gate below.
	consentBlocked := func(c *gin.Context) bool {
		v, ok := c.Get("auth_user_id")
		if !ok {
			return false
		}
		uid, ok := v.(int64)
		if !ok {
			return false
		}
		return !s.consentSvc.HasConsented(c.Request.Context(), uid)
	}
	api.Use(apipkg.ConsentGuard(consentBlocked))

	// Version header middleware
	api.Use(func(c *gin.Context) {
		c.Header("X-API-Version", "1")
		c.Header("X-Min-Client-Version", "1.0.0")
		c.Next()
	})

	// MCP internal API — no auth middleware.
	// Used by niuniu-mcp binary for tool calls. Localhost only — enforced by
	// LocalhostOnly middleware (see api/middleware.go).
	// MCPTokenAuth runs after LocalhostOnly: when a valid Bearer token is present
	// it resolves auth_user_id from the workspace's current_session_user_id so
	// that downstream resource handlers can enforce per-owner access.
	mcpGroup := s.engine.Group("/mcp")
	mcpGroup.Use(apipkg.LocalhostOnly())
	mcpGroup.Use(apipkg.MCPTokenAuth(s.mcpSessions, s.queries))
	{
		// License gate: block MCP writes when the deployment license is locked.
		// Reads (GET) still pass; the allowlist entries (/api/auth/*) never match
		// /mcp paths, so every non-GET MCP tool call is blocked in read-only mode.
		mcpGroup.Use(apipkg.LicenseGuard(func() bool {
			return s.licenseSvc.ReadOnly()
		}))

		// Consent gate on the AI execution interface: every non-GET MCP tool call
		// is blocked until the workspace session's user has accepted the
		// agreement. MCPTokenAuth has already resolved auth_user_id, so the shared
		// consentBlocked closure applies unchanged.
		mcpGroup.Use(apipkg.ConsentGuard(consentBlocked))

		// Project tools
		mcpGroup.GET("/projects/:id", s.projectHandler.Get)
		mcpGroup.GET("/projects/:id/columns", s.kanbanHandler.ListColumns)
		mcpGroup.GET("/projects/:id/issues", s.kanbanHandler.ListIssuesByProjectFiltered)
		mcpGroup.POST("/projects/:id/issues/batch", s.kanbanHandler.BatchCreateIssues)
		mcpGroup.GET("/issues/:id", s.kanbanHandler.GetIssue)
		mcpGroup.GET("/issues/:id/checklists", s.issueChecklistHandler.List)
		mcpGroup.GET("/issues/:id/comments", s.issueCommentHandler.List)

		// Issue write tools (delete_issue / update_issue + label / move /
		// lifecycle + checklist write). These reuse the exact same handler
		// methods as the /api/issues routes — MCPTokenAuth has already resolved
		// auth_user_id from the workspace's session, so each handler's
		// CanAccessIssue gate enforces the same multi-tenant ownership rules as
		// the UI (404 unknown issue, 403 cross-owner).
		mcpGroup.PUT("/issues/:id", s.kanbanHandler.UpdateIssue)
		// §23.5: deleting an issue is irreversible — force a user confirmation through
		// the ask-user broker even under autohost/bypassPermissions before it runs.
		mcpGroup.DELETE("/issues/:id",
			apipkg.IrreversibleOpGate(s.askUserService, s.queries, "delete_issue"),
			s.kanbanHandler.DeleteIssue)
		mcpGroup.PUT("/issues/:id/move", s.kanbanHandler.MoveIssue)
		mcpGroup.PUT("/issues/:id/lifecycle", s.kanbanHandler.UpdateIssueLifecycle)
		// Executable Epic: set parent/issue_type/exec_wave/exec_status via MCP.
		mcpGroup.PUT("/issues/:id/exec-fields", s.kanbanHandler.SetIssueExecFields)
		// Executable Epic mode B: orchestration agent dispatches a child workspace.
		mcpGroup.POST("/issues/:id/start-workspace", s.epicExecHandler.StartWorkspace)
		// AI-native board: agent self-reports progress by moving a card; an
		// `instruct` destination ensures the workspace + sends the column command.
		mcpGroup.POST("/issues/:id/advance", s.epicExecHandler.AdvanceIssue)
		// AI-native board: agent declares it cannot/should not do this issue and
		// parks it back in the backlog with a reason (abandoned-with-reason, §19).
		mcpGroup.POST("/issues/:id/abandon", s.epicExecHandler.AbandonIssue)
		// Autohost 安全网: hidden-ref checkpoint timeline / diff / revert / manual
		// snapshot, workspace-scoped so the agent needs only its own workspace id.
		mcpGroup.GET("/workspaces/:id/checkpoints", s.checkpointHandler.TimelineForWorkspace)
		mcpGroup.GET("/workspaces/:id/checkpoints/:cid/diff", s.checkpointHandler.DiffForWorkspace)
		mcpGroup.POST("/workspaces/:id/checkpoints/revert", s.checkpointHandler.RevertForWorkspace)
		mcpGroup.POST("/workspaces/:id/checkpoints", s.checkpointHandler.CreateForWorkspace)
		// Review 闭环 (#623): reviewer marks 需修改; bounce back to implement lane and
		// inject the two-layer review context (issue comments + unresolved diff comments).
		mcpGroup.POST("/issues/:id/request-changes", s.epicExecHandler.RequestChanges)
		mcpGroup.PUT("/issues/:id/labels", s.issueHandler.SetLabels)
		mcpGroup.POST("/issues/:id/checklists", s.issueChecklistHandler.Create)
		// Checklist update/toggle/delete keys off the checklist row id (it resolves
		// the parent issue for authz internally), mirroring /api/checklists/:checklistId.
		mcpGroup.PUT("/checklists/:checklistId", s.issueChecklistHandler.Update)
		mcpGroup.DELETE("/checklists/:checklistId", s.issueChecklistHandler.Delete)

		// Batch issue mutations (MCP-side mirrors of /api/issues/batch/*).
		mcpGroup.POST("/issues/batch/move", s.kanbanHandler.BatchMoveIssues)
		mcpGroup.POST("/issues/batch/priority", s.kanbanHandler.BatchUpdatePriority)
		// §23.5: batch delete is irreversible (hard-delete, no recovery) — same forced
		// user confirmation as single delete.
		mcpGroup.POST("/issues/batch/delete",
			apipkg.IrreversibleOpGate(s.askUserService, s.queries, "batch_delete_issues"),
			s.kanbanHandler.BatchDeleteIssues)
		mcpGroup.POST("/issues/batch/labels", s.labelHandler.BatchSetIssueLabels)

		// Local-runner tool dispatch (Epic #526 子B): the niuniu-mcp local_exec /
		// local_read / local_sync tools tunnel here. Workspace is resolved from the
		// session token (mcp_workspace_id), never a client-supplied id, so an agent
		// can only reach its own workspace's desktop runner.
		mcpGroup.GET("/local-runner/available", s.localRunnerHandler.MCPAvailable)
		mcpGroup.POST("/local-runner/exec", s.localRunnerHandler.MCPExec)
		mcpGroup.POST("/local-runner/read", s.localRunnerHandler.MCPRead)
		mcpGroup.POST("/local-runner/sync", s.localRunnerHandler.MCPSync)

		// Workspace tools
		mcpGroup.GET("/workspaces/:id/team/blackboard", s.teamHandler.ListBlackboard)
		mcpGroup.POST("/workspaces/:id/team/blackboard", s.teamHandler.WriteBlackboard)
		mcpGroup.GET("/workspaces/:id/project-id", s.memoryHandler.GetWorkspaceProjectID)

		// White-box memory (owner derived from the workspace).
		mcpGroup.GET("/workspaces/:id/memory", s.memoryHandler.MCPList)
		mcpGroup.POST("/workspaces/:id/memory/generate", s.memoryHandler.MCPGenerate)
		mcpGroup.POST("/workspaces/:id/memory/extract", s.memoryHandler.MCPExtract)
		mcpGroup.POST("/workspaces/:id/memory/consolidate", s.memoryHandler.MCPConsolidate)
		// Reversible memory mutation (soft-delete + versioned): lets an agent tidy
		// its owner's memory library (update / archive / restore).
		mcpGroup.POST("/workspaces/:id/memory/update", s.memoryHandler.MCPUpdateMemory)
		mcpGroup.POST("/workspaces/:id/memory/delete", s.memoryHandler.MCPDeleteMemory)
		mcpGroup.POST("/workspaces/:id/memory/restore", s.memoryHandler.MCPRestoreMemory)

		// Knowledge base (KB base3): owner-scoped + project-bound FTS search.
		// kb_list discovers the KBs a workspace can see; kb_search runs keyword
		// retrieval across them. Owner + project derived from the workspace token.
		mcpGroup.GET("/workspaces/:id/kb/list", s.kbHandler.MCPList)
		mcpGroup.GET("/workspaces/:id/kb/search", s.kbHandler.MCPSearch)

		// Harness gate — gate-check / checks / pre-commit (gate system, kept)
		mcpGroup.POST("/workspaces/:id/harness/gate-check", s.harnessHandler.RunGateCheck)
		mcpGroup.GET("/workspaces/:id/harness/checks", s.harnessHandler.ListChecks)
		mcpGroup.POST("/workspaces/:id/harness/pre-commit-check", s.harnessHandler.PreCommitCheck)

		// Managed tasks — the agent posts here (via the create_managed_task tool)
		// to provision a recurring task in one call: backing issue + no-repo
		// workspace + bound cron schedule.
		mcpGroup.POST("/managed-tasks", s.assistantHandler.CreateManagedTask)

		// Inbox (moved from niuniu-mcp binary; see service.InboxService)
		mcpGroup.POST("/inbox/send", s.teamHandler.InboxSend)
		mcpGroup.POST("/inbox/read", s.teamHandler.InboxRead)

		// Permission prompt — niuniu-mcp shim posts here for every Claude CLI
		// tool call gated by --permission-prompt-tool. Blocks until the user
		// answers in chat (or the request hits its 2h timeout).
		mcpGroup.POST("/permission-prompt", s.mcpPermissionHandler.Prompt)

		// Ask-user-question — niuniu-mcp shim posts here when the agent calls
		// niuniu_ask_user_question (substitute for Claude's built-in
		// AskUserQuestion which has no TTY in our non-PTY chat). Same
		// blocking semantics as permission-prompt.
		mcpGroup.POST("/ask-user-question", s.mcpAskUserHandler.Prompt)

		// External credentials — MCP tools need provider connectivity status.
		mcpGroup.GET("/external-credentials", s.extCredHandler.List)

		// External-source bindings (read-only for AI). Lets list_external_sources
		// discover which GitHub repo / TAPD workspace / Jira project this
		// niuniu project maps to, so the agent can route call_external_api
		// to the right provider + source_key.
		mcpGroup.GET("/projects/:id/external-sources", s.extSourceHandler.List)

		// IM Bot onboarding MCP tools (Epic #555 T4): five project-scoped tools
		// for the guidance agent. All enforce CanAccessProject via the :id param
		// (must share the same wildcard name as the other /mcp/projects/:id/* routes).
		mcpGroup.POST("/projects/:id/imbot/onboarding-token", s.imbotHandler.MCPRequestCredentialLink)
		mcpGroup.POST("/projects/:id/imbot/channels/:cid/test", s.imbotHandler.MCPTestChannel)
		mcpGroup.GET("/projects/:id/imbot/pending-chats", s.imbotHandler.MCPListPendingChats)
		mcpGroup.POST("/projects/:id/imbot/chats/:chatid/approve", s.imbotHandler.MCPApproveChat)
		mcpGroup.GET("/projects/:id/imbot/channels/:cid/status", s.imbotHandler.MCPChannelStatus)

		// External API proxy — universal MCP tool for AI-driven external
		// API calls. Replaces all old L4 work-item MCP tools.
		mcpGroup.POST("/external-proxy/call", s.proxyCall)
		mcpGroup.GET("/external-proxy/providers", s.proxyListProviders)
		mcpGroup.GET("/external-proxy/providers/:provider/schema", s.proxyGetProviderSchema)

		// Data integration proxy — run_data_query / list_data_sources tunnel here.
		// Three-layer gate + audit lives in DataProxyService.
		mcpGroup.POST("/data-proxy/query", s.dataProxyQuery)
		mcpGroup.GET("/data-proxy/sources", s.dataProxySources)
		// create_data_source — create a source owned by the workspace owner and
		// auto-bind it to the workspace project (so it is immediately queryable).
		mcpGroup.POST("/data-proxy/sources", s.dataProxyCreateSource)

		// Data dashboards — pin_query / list_dashboards tunnel here. pin_query
		// injects the current workspace (from the session token) as the saved
		// query's origin workspace_id; the agent never passes it.
		mcpGroup.POST("/dashboards/pin", s.dashboardPin)
		mcpGroup.GET("/dashboards", s.dashboardList)
	}

	// Auth me (requires auth)
	api.GET("/auth/me", s.authHandler.Me)
	api.POST("/auth/logout", s.authHandler.Logout)
	api.POST("/auth/password/change", s.authHandler.ChangePassword)
	api.GET("/auth/login-history", s.authHandler.LoginHistory)
	api.POST("/auth/mfa/setup", s.mfaHandler.Setup)
	api.POST("/auth/mfa/enable", s.mfaHandler.Enable)
	api.POST("/auth/mfa/disable", s.mfaHandler.Disable)
	api.GET("/auth/mfa/status", s.mfaHandler.Status)
	api.POST("/auth/mfa/backup-codes/regenerate", s.mfaHandler.RegenerateBackupCodes)
	api.POST("/auth/mfa/reset/:id", auth.RequireRole("admin"), s.mfaHandler.AdminResetMFA)
	api.GET("/auth/users", auth.RequireRole("admin"), s.authHandler.ListUsers)
	api.POST("/auth/users", auth.RequireRole("admin"),
		apipkg.LicenseSeatGate(func(c *gin.Context) error {
			return s.licenseSvc.CheckSeatAvailable(c.Request.Context())
		}),
		s.authHandler.CreateUser)
	api.PATCH("/auth/users/:id", auth.RequireRole("admin"), s.authHandler.UpdateUser)
	api.POST("/auth/users/:id/password", auth.RequireRole("admin"), s.authHandler.ResetPassword)
	api.DELETE("/auth/users/:id", auth.RequireRole("admin"), s.authHandler.DeleteUserByID)
	// Admin: view/delete a user's personal resources + one-click account purge.
	api.GET("/auth/users/:id/resources", auth.RequireRole("admin"), s.adminUserHandler.GetResources)
	api.DELETE("/auth/users/:id/resources/:type/:resourceId", auth.RequireRole("admin"), s.adminUserHandler.DeleteResource)
	api.POST("/auth/users/:id/purge", auth.RequireRole("admin"), s.adminUserHandler.Purge)

	// Users search
	api.GET("/users/search", s.usersHandler.Search)
	// Owner-grain token usage time series (summed across the owner's workspaces).
	api.GET("/token-usage", s.tokenUsageHandler.OwnerUsage)

	// Org routes
	// 多租户组织（Tier 1）是功能分级能力：开源个人版禁用（NopGate.FeatureEnabled
	// 对 FeatureOrg 返回 false），企业版按 license 开启。禁用时整个 /orgs 组 403。
	orgs := api.Group("/orgs")
	{
		orgs.Use(apipkg.LicenseFeatureGate(func(f string) bool {
			return s.licenseSvc.FeatureEnabled(f)
		}, license.FeatureOrg))
		orgs.POST("", s.orgHandler.CreateOrg)
		orgs.GET("", s.orgHandler.ListOrgsForUser)
		orgs.GET("/all", s.orgHandler.ListAllOrgs) // global admin: every org
		orgs.GET("/:id", s.orgHandler.GetOrg)
		orgs.PATCH("/:id", s.orgHandler.UpdateOrg)
		orgs.DELETE("/:id", s.orgHandler.DeleteOrg)
		orgs.GET("/:id/members", s.orgHandler.ListMembers)
		orgs.POST("/:id/members", s.orgHandler.AddMember)
		orgs.PATCH("/:id/members/:user_id", s.orgHandler.UpdateMemberRole)
		orgs.DELETE("/:id/members/:user_id", s.orgHandler.RemoveMember)
		orgs.POST("/:id/transfer-ownership", s.orgHandler.TransferOwnership)
		orgs.GET("/:id/audit-log", s.orgHandler.ListAuditLog)
	}

	// Me routes
	me := api.Group("/me")
	{
		me.GET("/orgs", s.meHandler.ListOrgs)

		// "需要我处理的" view (spec §19): issues in a terminal needs-human exec_status
		// (blocked-needs-human / waiting-user-input / abandoned) across accessible owners.
		me.GET("/attention-issues", s.kanbanHandler.ListAttentionIssues)

		// External provider credentials (caller-scoped). v1 is personal-
		// owner only; org-scoped credentials wait until the SPA exposes
		// an org-context switcher (see ExternalCredentialHandler.resolveCallerOwner).
		me.GET("/external-credentials", s.extCredHandler.List)
		me.POST("/external-credentials", s.extCredHandler.Create)
		me.DELETE("/external-credentials/:id", s.extCredHandler.Delete)
		me.PATCH("/external-credentials/:id", s.extCredHandler.Patch)
		me.GET("/external-credentials/:id/usages", s.extCredHandler.Usages)
		me.POST("/external-credentials/:id/verify-imap", s.extCredHandler.VerifyImap)

		// External API proxy — generic HTTP pass-through for AI-driven external API calls.
		me.POST("/external-proxy/call", s.proxyCall)
		me.GET("/external-proxy/providers", s.proxyListProviders)
		me.POST("/external-proxy/providers", s.proxyCreateProvider)
		me.GET("/external-proxy/providers/:provider/schema", s.proxyGetProviderSchema)
		me.PUT("/external-proxy/providers/:id", s.proxyUpdateProvider)
		me.DELETE("/external-proxy/providers/:id", s.proxyDeleteProvider)
		me.PATCH("/external-proxy/providers/:id/write-enabled", s.proxySetWriteEnabled)

		// Data integration (M1): owner-scoped SQL data sources. CRUD + verify.
		// Spec: docs/superpowers/specs/2026-06-04-data-integration-and-dashboard-design.md
		me.GET("/data-sources", s.dataSourceHandler.List)
		me.POST("/data-sources", s.dataSourceHandler.Create)
		me.PATCH("/data-sources/:id", s.dataSourceHandler.Update)
		me.DELETE("/data-sources/:id", s.dataSourceHandler.Delete)
		me.POST("/data-sources/:id/verify", s.dataSourceHandler.Verify)

		// Knowledge bases (Epic #496): owner-scoped corpora — CRUD, async
		// ingest (local/upload/url), browse + keyword search, retry/mirror.
		me.GET("/knowledge-bases", s.knowledgeBaseHandler.List)
		me.POST("/knowledge-bases", s.knowledgeBaseHandler.Create)
		me.GET("/knowledge-bases/:id", s.knowledgeBaseHandler.Get)
		me.PATCH("/knowledge-bases/:id", s.knowledgeBaseHandler.Update)
		me.DELETE("/knowledge-bases/:id", s.knowledgeBaseHandler.Delete)
		me.POST("/knowledge-bases/:id/retry", s.knowledgeBaseHandler.Retry)
		me.POST("/knowledge-bases/:id/files", s.knowledgeBaseHandler.Upload)
		me.GET("/knowledge-bases/:id/documents", s.knowledgeBaseHandler.ListDocuments)
		me.GET("/knowledge-bases/:id/search", s.knowledgeBaseHandler.Search)
		me.GET("/kb-presets", s.knowledgeBaseHandler.Presets)

		// Per-user git author identity (Phase 0). The user-editable email
		// surfaces on git commits niuniu makes on their behalf. Spec:
		// docs/superpowers/specs/2026-05-19-per-user-git-identity-design.md
		me.GET("/git-identity", s.gitIdentityHandler.Get)
		me.PUT("/git-identity", s.gitIdentityHandler.Put)

		// Per-(user, repository) git identity overrides (v5.1 §3.1.6).
		// Different repos can use different author names/emails.
		me.GET("/repository-identities", s.gitIdentityHandler.ListRepositoryIdentities)
		me.GET("/repository-identities/:repo_id", s.gitIdentityHandler.GetRepositoryIdentity)
		me.PUT("/repository-identities/:repo_id", s.gitIdentityHandler.PutRepositoryIdentity)
		me.DELETE("/repository-identities/:repo_id", s.gitIdentityHandler.DeleteRepositoryIdentity)

		// Per-user git remote SSH credentials (v5 Phase 2 sub-phase A).
		// Used by sub-phase B (next session) to materialize keys into the
		// PTY's GIT_SSH_COMMAND so Claude can `git push` as the user.
		me.GET("/git-remote-credentials", s.gitRemoteCredHandler.List)
		me.POST("/git-remote-credentials", s.gitRemoteCredHandler.Create)
		me.DELETE("/git-remote-credentials/:id", s.gitRemoteCredHandler.Delete)
	}

	// Config endpoints
	// Directory listing
	api.GET("/directories", s.directoryHandler.List)
	api.GET("/system-info", s.directoryHandler.SystemInfo)
	// Local-directory browser (personal edition only) for the knowledge picker.
	api.GET("/fs/list-dirs", s.fsHandler.ListDirs)
	api.GET("/system-deps", s.systemDepsHandler.Probe)
	api.POST("/system-deps/install", auth.RequireRole("admin"), s.systemDepsHandler.Install)
	api.GET("/system-deps/install/stream", auth.RequireRole("admin"), s.systemDepsHandler.Stream)
	api.POST("/system-deps/git-identity", auth.RequireRole("admin"), s.systemDepsHandler.SetGitIdentity)

	// Slash commands discovery
	api.GET("/slash-commands", s.slashCommandHandler.ListCommands)

	// License status -- any authenticated user (drives the SPA banner).
	api.GET("/license/status", s.licenseHandler.GetStatus)

	// Consent status + accept -- any authenticated user (drives the SPA consent
	// gate). Both paths are on the ConsentGuard allowlist so an un-consented
	// user can still read the agreement state and accept it.
	api.GET("/consent/status", s.consentHandler.GetStatus)
	api.POST("/consent/accept", s.consentHandler.Accept)

	// Admin-only settings (Phase 2 T2.4). RequireAdmin is a pass-through in
	// personal mode (single user = admin); team mode requires admin/owner role.
	adminGroup := api.Group("/admin")
	adminGroup.Use(auth.RequireAdmin(s.cfg.Auth.Enabled))
	adminGroup.GET("/settings/:key", s.adminSettingsHandler.GetSetting)
	adminGroup.PUT("/settings/:key", s.adminSettingsHandler.PutSetting)

	// License management -- super-admin only.
	adminGroup.GET("/license", s.licenseHandler.GetAdminDetail)
	adminGroup.POST("/license", s.licenseHandler.Install)

	// SSE event stream for desktop client
	api.GET("/events/stream", s.eventsHandler.Stream)

	api.GET("/config", s.configHandler.Get)
	api.PUT("/config", s.configHandler.Update)

	// Project routes
	projects := api.Group("/projects")
	{
		projects.GET("", s.projectHandler.List)
		projects.POST("", s.projectHandler.Create)
		projects.GET("/:id", s.projectHandler.Get)
		projects.PUT("/:id", s.projectHandler.Update)
		projects.DELETE("/:id", s.projectHandler.Delete)
		projects.PUT("/:id/status", s.projectHandler.UpdateStatus)
		projects.PUT("/:id/color", s.projectHandler.UpdateColor)
		projects.PUT("/:id/default-cli-type", s.projectHandler.UpdateDefaultCliType)
		projects.PUT("/:id/env-provider", s.projectHandler.UpdateEnvProvider)

		// Column routes under project
		projects.GET("/:id/columns", s.kanbanHandler.ListColumns)
		projects.POST("/:id/columns", s.kanbanHandler.CreateColumn)
		projects.GET("/:id/issues", s.kanbanHandler.ListIssuesByProject)

		// Save this project (its columns + default scenes) as a reusable
		// blueprint / template. List + delete live at the top-level
		// /project-blueprints group below.
		projects.POST("/:id/blueprints", s.projectBlueprintHandler.SaveFromProject)

		// Project repository bindings
		projects.GET("/:id/repositories", s.projectHandler.ListRepositories)
		projects.POST("/:id/repositories", s.projectHandler.AddRepository)
		projects.DELETE("/:id/repositories/:repoID", s.projectHandler.RemoveRepository)
		projects.PATCH("/:id/repositories/:repoID", s.projectHandler.UpdateRepository)

		// Labels (Task 10): project-scoped CRUD. Update/Delete are admin-gated
		// inside the service.
		projects.GET("/:id/labels", s.labelHandler.List)
		projects.POST("/:id/labels", s.labelHandler.Create)
		projects.PATCH("/:id/labels/:label_id", s.labelHandler.Update)
		projects.DELETE("/:id/labels/:label_id", s.labelHandler.Delete)

		// Assignable users (Task 11): owner-resolved member list for the
		// issue-detail assignee picker.
		projects.GET("/:id/assignable-users", s.projectHandler.ListAssignableUsers)

		// External-source bindings (project ↔ GitHub repo / TAPD workspace / Jira).
		// Browse / import are no longer exposed — AI handles those through MCP
		// external-proxy after reading the binding here.
		projects.GET("/:id/external-sources", s.extSourceHandler.List)
		projects.POST("/:id/external-sources", s.extSourceHandler.Add)
		projects.DELETE("/:id/external-sources/:sid", s.extSourceHandler.Delete)

		// Project-scoped data-source (dataconn) association: bind/unbind the
		// caller's data sources to this project so the project's agents see them.
		projects.GET("/:id/data-sources", s.dataSourceHandler.ListProjectSources)
		projects.POST("/:id/data-sources", s.dataSourceHandler.AddProjectSource)
		projects.DELETE("/:id/data-sources/:sid", s.dataSourceHandler.RemoveProjectSource)

		// Project-scoped knowledge-base association (Epic #496).
		projects.GET("/:id/knowledge-bases", s.knowledgeBaseHandler.ListProjectKBs)
		projects.POST("/:id/knowledge-bases", s.knowledgeBaseHandler.AddProjectKB)
		projects.DELETE("/:id/knowledge-bases/:kbid", s.knowledgeBaseHandler.RemoveProjectKB)

		// IM Bot remote channels + chat pairing (Epic #555), all project-scoped.
		projects.GET("/:id/imbot/channels", s.imbotHandler.ListChannels)
		projects.POST("/:id/imbot/channels", s.imbotHandler.CreateChannel)
		projects.PATCH("/:id/imbot/channels/:cid", s.imbotHandler.UpdateChannel)
		projects.DELETE("/:id/imbot/channels/:cid", s.imbotHandler.DeleteChannel)
		projects.POST("/:id/imbot/channels/:cid/test", s.imbotHandler.TestChannel)
		projects.POST("/:id/imbot/channels/:cid/chats", s.imbotHandler.AddChat)
		projects.GET("/:id/imbot/chats", s.imbotHandler.ListChats)
		projects.POST("/:id/imbot/chats/:chatid/approve", s.imbotHandler.ApproveChat)
		projects.PATCH("/:id/imbot/chats/:chatid", s.imbotHandler.PatchChat)
		projects.DELETE("/:id/imbot/chats/:chatid", s.imbotHandler.DeleteChat)
		// AI-onboarding start (Epic #555 T3): create guidance issue + workspace,
		// seed the kickoff prompt. Authenticated + write-access gated in the handler.
		projects.POST("/:id/imbot/onboarding", s.imbotHandler.StartOnboarding)
		// Direct WeChat 微信ClawBot connect: mint a one-time QR-scan onboarding link
		// so the settings UI can jump straight to the scan page (no chat step).
		projects.POST("/:id/imbot/wechat-link", s.imbotHandler.IssueWechatLink)

		// Automatic memory-staleness visibility: the review queue and the
		// execution log of automatic sweeps.
		projects.GET("/:id/memory-review-queue", s.memoryHandler.ListReviewQueue)
		projects.GET("/:id/memory-sweep-runs", s.memoryHandler.ListSweepRuns)
		projects.DELETE("/:id/memory-sweep-runs", s.memoryHandler.ClearSweepRuns)
		// Per-project automatic-maintenance schedule (cron; empty = OFF).
		projects.GET("/:id/memory-schedule", s.memoryHandler.GetProjectMemorySchedule)
		projects.PUT("/:id/memory-schedule", s.memoryHandler.SetProjectMemorySchedule)
		// Manual "run once" maintenance trigger.
		projects.POST("/:id/memory-maintenance/run", s.memoryHandler.RunMemoryMaintenanceOnce)
		// Per-project workspace auto-cleanup policy (OFF by default).
		projects.GET("/:id/cleanup-policy", s.cleanupHandler.GetCleanupPolicy)
		projects.PUT("/:id/cleanup-policy", s.cleanupHandler.SetCleanupPolicy)
		// Manual "clean now" sweep trigger.
		projects.POST("/:id/cleanup/run", s.cleanupHandler.RunCleanupOnce)

	}

	// Lifecycle groups (registered before /columns group to avoid /:id conflict)
	api.GET("/columns/lifecycle-groups", s.kanbanHandler.ListLifecycleGroups)

	// Column routes
	columns := api.Group("/columns")
	{
		columns.GET("/:id/issues", s.kanbanHandler.ListIssues)
		columns.POST("/:id/issues", s.kanbanHandler.CreateIssue)
		columns.PUT("/:id", s.kanbanHandler.UpdateColumn)
		columns.PUT("/:id/position", s.kanbanHandler.UpdateColumnPosition)
		columns.DELETE("/:id", s.kanbanHandler.DeleteColumn)
		columns.PUT("/:id/lifecycle-mapping", s.kanbanHandler.UpdateColumnLifecycleMapping)
		columns.PUT("/:id/extension", s.kanbanHandler.UpdateColumnExtension)
		columns.GET("/:id/gate-specs", s.kanbanHandler.ListColumnGateSpecs)
		columns.PUT("/:id/gate-specs", s.kanbanHandler.ReplaceColumnGateSpecs)
	}

	// Issue routes
	issues := api.Group("/issues")
	{
		issues.GET("/:id", s.kanbanHandler.GetIssue)
		issues.PUT("/:id", s.kanbanHandler.UpdateIssue)
		issues.PUT("/:id/move", s.kanbanHandler.MoveIssue)
		issues.PUT("/:id/lifecycle", s.kanbanHandler.UpdateIssueLifecycle)
		// Executable Epic: set parent/issue_type/exec_wave/exec_status.
		issues.PUT("/:id/exec-fields", s.kanbanHandler.SetIssueExecFields)
		// Executable Epic: read derived progress (done/total/exec_status). The mode-A
		// wave-engine execute/pause/resume routes were retired with that engine; an
		// epic is now driven by its orchestration agent (workspace on the epic issue).
		issues.GET("/:id/epic-progress", s.epicExecHandler.GetEpicProgress)
		// Executable Epic (P2): human merge to main -> control workspace agent.
		issues.POST("/:id/merge-to-main", s.epicExecHandler.MergeToMain)
		// Executable Epic mode B: dispatch a workspace for an issue.
		issues.POST("/:id/start-workspace", s.epicExecHandler.StartWorkspace)
		// AI-suggest goal_condition: spawns `claude -p`, per-user rate-limited
		// (6 req/min, burst 3) to bound subprocess cost on the abuse model
		// described in 2026-05-14-issue-goal-condition-ai-suggest-design.md.
		issues.POST("/:id/suggest-goal-condition",
			auth.RateLimitUserMiddleware(0.1, 3),
			s.kanbanHandler.SuggestGoalCondition)
		issues.DELETE("/:id", s.kanbanHandler.DeleteIssue)
		issues.POST("/:id/workspace", s.workspaceHandler.CreateForIssue)
		issues.GET("/:id/workspace", s.workspaceHandler.GetByIssue)
		issues.GET("/:id/checklists", s.issueChecklistHandler.List)
		issues.POST("/:id/checklists", s.issueChecklistHandler.Create)
		issues.GET("/:id/comments", s.issueCommentHandler.List)
		issues.POST("/:id/comments", s.issueCommentHandler.Create)
		// Review 闭环 (#623): human reviewer marks 需修改 → bounce back to implement
		// lane with the two-layer review context injected into the agent's continuation.
		issues.POST("/:id/request-changes", s.epicExecHandler.RequestChanges)
		issues.GET("/:id/timeline", s.issueTimelineHandler.GetTimeline)
		// Per-issue execution timeline (spec §23.7): advance / gate / ask_user /
		// terminal / intervention / cost events + cumulative cost.
		issues.GET("/:id/exec-timeline", s.issueTimelineHandler.GetExecTimeline)

		// Subresource setters (Task 12): replace the full assignees / labels set.
		issues.PUT("/:id/assignees", s.issueHandler.SetAssignees)
		issues.PUT("/:id/labels", s.issueHandler.SetLabels)

		// Batch issue mutations (kanban bulk-actions toolbar + MCP #249).
		// Each handler authorizes per-id via Authz.CanAccessIssue and
		// returns {succeeded, skipped:[{id,reason}]} (HTTP 200 always).
		issues.POST("/batch/move", s.kanbanHandler.BatchMoveIssues)
		issues.POST("/batch/priority", s.kanbanHandler.BatchUpdatePriority)
		issues.POST("/batch/delete", s.kanbanHandler.BatchDeleteIssues)
		issues.POST("/batch/labels", s.labelHandler.BatchSetIssueLabels)

	}

	checklists := api.Group("/checklists")
	{
		checklists.PUT("/:checklistId", s.issueChecklistHandler.Update)
		checklists.PUT("/:checklistId/position", s.issueChecklistHandler.UpdatePosition)
		checklists.DELETE("/:checklistId", s.issueChecklistHandler.Delete)
	}

	issueComments := api.Group("/issue-comments")
	{
		issueComments.PUT("/:commentId", s.issueCommentHandler.Update)
		issueComments.DELETE("/:commentId", s.issueCommentHandler.Delete)
	}

	// Workspace routes
	workspaces := api.Group("/workspaces")
	{
		// Per-workspace MCP config endpoints (Phase 2)
		workspaces.POST("/mcp/detect", s.workspaceMCPHandler.Detect)
		workspaces.GET("/:id/mcp", s.workspaceMCPHandler.Get)
		workspaces.PUT("/:id/mcp", s.workspaceMCPHandler.Put)
		workspaces.PUT("/:id/mcp/strict", s.workspaceMCPHandler.PutStrict)
		workspaces.POST("/:id/mcp/redetect", s.workspaceMCPHandler.Redetect)

		// Per-workspace quick actions: caller's personal/org actions plus the
		// scene-provided group parsed live from the projection (not persisted).
		workspaces.GET("/:id/quick-actions", s.quickActionHandler.ListForWorkspace)

		// Autohost 安全网: hidden-ref checkpoint timeline / per-step diff / one-click
		// revert, bound to the WORKSPACE (checkpoints are a per-worktree git concept).
		// Authorized via CanAccessWorkspace; same handlers back the /mcp twin.
		workspaces.GET("/:id/checkpoints", s.checkpointHandler.TimelineForWorkspace)
		workspaces.GET("/:id/checkpoints/:cid/diff", s.checkpointHandler.DiffForWorkspace)
		workspaces.POST("/:id/checkpoints/revert", s.checkpointHandler.RevertForWorkspace)

		workspaces.GET("", s.workspaceHandler.List)
		// Lazy git badges for the sidebar; literal segment before /:id.
		workspaces.GET("/sidebar-git", s.workspaceHandler.SidebarGitStatus)
		// Overview must come before /:id so the literal segment isn't shadowed.
		workspaces.GET("/overview", s.workspaceHandler.Overview)
		workspaces.GET("/overview/creators", s.workspaceHandler.OverviewCreators)
		// Per-user pinned workspace ids (literal segment, must precede /:id).
		workspaces.GET("/pins", s.workspaceHandler.ListPins)
		// Literal segment must precede /:id so it is not shadowed.
		workspaces.GET("/search", s.workspaceHandler.SearchContent)
		workspaces.POST("", s.workspaceHandler.Create)
		// Literal segment must precede /:id so it is not shadowed.
		workspaces.POST("/from-directory", s.workspaceHandler.CreateFromDirectory)
		workspaces.GET("/available-issues", s.workspaceHandler.ListAvailableIssues)
		workspaces.GET("/issue-defaults", s.workspaceHandler.GetIssueDefaults)
		workspaces.GET("/archived", s.workspaceHandler.ListArchived)
		workspaces.POST("/batch-delete", s.workspaceHandler.BatchDelete)
		workspaces.GET("/:id", s.workspaceHandler.Get)
		workspaces.GET("/:id/token-usage", s.tokenUsageHandler.WorkspaceUsage)
		workspaces.DELETE("/:id", s.workspaceHandler.Delete)
		workspaces.GET("/:id/tree", s.workspaceHandler.GetTree)
		// Drawer-style tree endpoints - order matters! /tree/main must come before /tree/worktrees/*
		workspaces.GET("/:id/tree/main", s.workspaceHandler.GetMainTree)
		workspaces.GET("/:id/tree/worktrees/*name", s.workspaceHandler.GetWorktreeTree)
		workspaces.GET("/:id/tree/groups", s.workspaceHandler.ListWorktreeGroups)

		// Repository management within workspace
		workspaces.GET("/:id/repositories", s.workspaceHandler.ListRepositories)
		workspaces.POST("/:id/repositories", s.workspaceHandler.AddRepository)

		// Knowledge-base mounting (KB as a first-class citizen): explicit
		// per-workspace mounts materialized read-only into datasets/<name>.
		workspaces.GET("/:id/kbs", s.knowledgeBaseHandler.ListWorkspaceKBs)
		workspaces.POST("/:id/kbs", s.knowledgeBaseHandler.MountWorkspaceKB)
		workspaces.POST("/:id/kbs/:kbid/sync", s.knowledgeBaseHandler.SyncWorkspaceKB)
		workspaces.DELETE("/:id/kbs/:kbid", s.knowledgeBaseHandler.UnmountWorkspaceKB)

		// Workspace settings
		workspaces.PUT("/:id/name", s.workspaceHandler.UpdateName)

		// Per-user overview pin (置顶): toggle top-of-list placement.
		workspaces.PUT("/:id/pin", s.workspaceHandler.Pin)
		workspaces.DELETE("/:id/pin", s.workspaceHandler.Unpin)

		// Environment variables
		workspaces.GET("/:id/env", s.workspaceHandler.GetEnv)
		workspaces.PUT("/:id/env", s.workspaceHandler.SetEnv)
		// Direct subscription-platform provider binding (issue #653 simplification)
		workspaces.PUT("/:id/env-provider", s.workspaceHandler.SetEnvProvider)

		// Local-runner binding (Epic #526 子B): read state / save+trigger / unbind.
		workspaces.GET("/:id/local-runner", s.localRunnerHandler.Get)
		workspaces.PUT("/:id/local-runner", s.localRunnerHandler.Put)
		workspaces.DELETE("/:id/local-runner", s.localRunnerHandler.Delete)

		// Agent routes under workspace
		workspaces.POST("/:id/agent/start", s.agentHandler.Start)
		workspaces.POST("/:id/agent/stop", s.agentHandler.Stop)
		workspaces.POST("/:id/agent/send", s.agentHandler.Send)
		workspaces.GET("/:id/agent/status", s.agentHandler.Status)

		// AgentProxy routes (new — AI CLI chat)
		workspaces.GET("/:id/messages", s.agentProxyHandler.ListMessages)
		workspaces.POST("/:id/messages", s.agentProxyHandler.SendMessage)
		workspaces.GET("/:id/session", s.agentProxyHandler.GetSession)
		workspaces.DELETE("/:id/session", s.agentProxyHandler.StopSession)
		workspaces.POST("/:id/session/clear", s.agentProxyHandler.ClearSession)
		workspaces.GET("/:id/costs", s.agentProxyHandler.GetCosts)
		workspaces.GET("/:id/claude-status", s.agentProxyHandler.GetClaudeStatus)

		// Attachments
		workspaces.POST("/:id/attachments", s.attachmentHandler.Upload)
		workspaces.DELETE("/:id/attachments/:name", s.attachmentHandler.Delete)
		workspaces.GET("/:id/file-content", s.attachmentHandler.FileContent)
		workspaces.POST("/:id/artifacts", s.attachmentHandler.AddArtifact)
		workspaces.DELETE("/:id/artifacts", s.attachmentHandler.RemoveArtifact)

		// File tree search
		workspaces.GET("/:id/files", s.fileTreeHandler.Search)

		// Queue routes
		workspaces.GET("/:id/queue", s.queueHandler.List)
		workspaces.POST("/:id/queue", s.queueHandler.Create)
		workspaces.PUT("/:id/queue/reorder", s.queueHandler.Reorder)
		workspaces.PUT("/:id/queue/:queueId", s.queueHandler.Update)
		workspaces.DELETE("/:id/queue/:queueId", s.queueHandler.Delete)

		// Schedule routes
		workspaces.GET("/:id/schedules", s.scheduleHandler.List)
		workspaces.POST("/:id/schedules", s.scheduleHandler.Create)
		workspaces.PUT("/:id/schedules/:scheduleId", s.scheduleHandler.Update)
		workspaces.DELETE("/:id/schedules/:scheduleId", s.scheduleHandler.Delete)
		workspaces.POST("/:id/schedules/:scheduleId/toggle", s.scheduleHandler.Toggle)
		workspaces.POST("/:id/schedules/:scheduleId/trigger", s.scheduleHandler.Trigger)

		// Workspace tasks
		workspaces.GET("/:id/workspace-tasks", s.workspaceTaskHandler.List)

		// Pinned chat messages — bookmarks rendered in the pin-message panel.
		workspaces.GET("/:id/pinned-messages", s.pinnedMessageHandler.List)
		workspaces.POST("/:id/pinned-messages", s.pinnedMessageHandler.Create)
		workspaces.DELETE("/:id/pinned-messages/:pinId", s.pinnedMessageHandler.Delete)

		// Changes summary
		workspaces.GET("/:id/changes-summary", s.workspaceHandler.GetChangesSummary)

		// Project-ID lookup + session memory extraction (workspace-keyed).
		workspaces.GET("/:id/project-id", s.memoryHandler.GetWorkspaceProjectID)
		workspaces.POST("/:id/extract-learnings", s.memoryHandler.ExtractSession)
		workspaces.GET("/:id/extract-learnings/status", s.memoryHandler.ExtractSessionStatus)

		// Worktree commit history
		workspaces.GET("/:id/worktrees/:name/commits", s.workspaceHandler.GetWorktreeCommits)
		workspaces.GET("/:id/worktrees/:name/commits/:hash", s.workspaceHandler.GetWorktreeCommitDetail)

		// Git operations on worktrees
		workspaces.POST("/:id/worktrees/:name/commit", s.workspaceOpsHandler.Commit)
		workspaces.POST("/:id/worktrees/:name/merge", s.workspaceOpsHandler.Merge)
		workspaces.POST("/:id/worktrees/:name/push", s.workspaceOpsHandler.Push)
		workspaces.POST("/:id/worktrees/:name/generate-commit-message", s.workspaceOpsHandler.GenerateCommitMessage)
		workspaces.PUT("/:id/worktrees/:name/file", s.workspaceOpsHandler.WriteWorktreeFile)

		// Workspace completion
		workspaces.POST("/:id/complete", s.workspaceOpsHandler.Complete)
		workspaces.POST("/:id/mark-done", s.workspaceOpsHandler.MarkDone)
		workspaces.POST("/:id/unmark-done", s.workspaceOpsHandler.UnmarkDone)

		// Workspace archive
		workspaces.POST("/:id/archive", s.workspaceHandler.Archive)

		// Repo-scoped git ops under workspace
		workspaces.GET("/:id/repositories/:repo_id/git/branches", s.gitOpsHandler.GetBranches)

		// Review routes under workspace
		workspaces.GET("/:id/diff", s.reviewHandler.GetDiff)
		workspaces.GET("/:id/repositories/:repo_id/diff", s.reviewHandler.GetRepoDiff)
		workspaces.GET("/:id/comments", s.reviewHandler.ListComments)
		workspaces.POST("/:id/comments", s.reviewHandler.CreateComment)

		// Permission prompt — chat-side endpoints back the in-chat dialog.
		workspaces.GET("/:id/permission-requests", s.permissionHandler.ListByWorkspace)
		workspaces.GET("/:id/permission-allowlist", s.permissionHandler.ListAllowlist)
		// Ask-user-question — chat-side endpoint to backfill pending requests
		// when a workspace mounts (same shape as permission-requests).
		workspaces.GET("/:id/ask-user-requests", s.askUserHandler.ListByWorkspace)
	}

	// Agent permission decisions and allowlist mutations live outside the
	// /workspaces group because they're keyed by request/entry id, not workspace.
	api.POST("/agent-permission-decisions/:requestID", s.permissionHandler.Decide)
	api.DELETE("/permission-allowlist/:entryID", s.permissionHandler.DeleteAllowlist)

	// Ask-user-question answer submission, keyed by request id.
	api.POST("/agent-ask-user-decisions/:requestID", s.askUserHandler.Decide)

	// Global schedule routes
	schedules := api.Group("/schedules")
	{
		schedules.GET("", s.scheduleHandler.ListAll)
		schedules.GET("/:scheduleId/runs", s.scheduleHandler.ListRuns)
	}

	// Relay account + mobile pairing.  These operate on OS-keychain state that
	// belongs to the server process (not the shell), so they live in the server
	// API so both browser and Wails clients can use them.
	relay := api.Group("/relay")
	{
		relay.GET("/status", s.relayHandler.GetStatus)
		relay.POST("/email-code/request", s.relayHandler.RequestEmailCode)
		relay.POST("/email-code/verify", s.relayHandler.VerifyEmailCode)
		relay.POST("/logout", s.relayHandler.Logout)

		relay.POST("/pairing/start", s.relayHandler.StartPairing)
		relay.GET("/pairing/status", s.relayHandler.PairingStatus)
		relay.POST("/pairing/confirm", s.relayHandler.ConfirmPairing)
		relay.POST("/pairing/reject", s.relayHandler.RejectPairing)

		relay.POST("/verify-email/request", s.relayHandler.RequestEmailVerification)

		relay.GET("/trusted-mobiles", s.relayHandler.ListTrustedMobiles)
		relay.DELETE("/trusted-mobiles/:xpubHex", s.relayHandler.RemoveTrustedMobile)

		// Billing + remote mobile management proxy the relay's /api/billing/*
		// and /api/mobile-devices/:id endpoints through the local scoped
		// client, so the desktop frontend only needs niuniu-server auth.
		relay.GET("/billing/usage", s.relayHandler.GetBillingUsage)
		relay.GET("/billing/my-plan", s.relayHandler.GetBillingPlan)
		relay.GET("/billing/plans", s.relayHandler.ListBillingPlans)
		relay.POST("/billing/change-plan", s.relayHandler.ChangeBillingPlan)

		relay.GET("/mobile-devices", s.relayHandler.ListRemoteMobileDevices)
		relay.DELETE("/mobile-devices/:id", s.relayHandler.RevokeRemoteMobileDevice)

		// Account's desktops on the relay — separate from the mobile list,
		// proxied through the same scoped client. Lets the device-list UI
		// show desktops alongside mobiles so stale registrations from
		// earlier installs are visible and revocable in one place.
		relay.GET("/desktops", s.relayHandler.ListRemoteAccountDesktops)
		relay.DELETE("/desktops/:id", s.relayHandler.RevokeRemoteAccountDesktop)
	}

	// Shell utilities — open an external URL in the host's default browser.
	// Lets the settings UI hand Stripe checkout off to Chrome/Edge/Safari
	// rather than load it inside the embedded webview where cookies and
	// payment form plugins may behave oddly.
	// Launch-at-login toggle for the personal/bundled desktop edition. Reports
	// supported=false in team/hosted mode so the SPA hides the control.
	api.GET("/autostart", s.autostartHandler.Get)
	api.PUT("/autostart", s.autostartHandler.Put)

	api.POST("/shell/open-external", s.shellHandler.OpenExternal)
	// Reveal a workspace/repository path in the host OS file manager.
	// Personal-edition only — meaningless on hosted deployments because the
	// path lives on the server host, not the user's machine.
	api.POST("/shell/open-path", s.shellHandler.OpenPath)
	// Open a new OS-native terminal in the user's home dir and run `claude`.
	// Personal-edition only; the handler returns 403 in hosted deployments.
	api.POST("/shell/claude-login", s.shellHandler.ClaudeLogin)
	// Codex twin: opens a terminal and runs `codex login`. Same personal-mode
	// gate. Surfaced from system-deps and the Codex accounts default row.
	api.POST("/shell/codex-login", s.shellHandler.CodexLogin)
	// Raise the native AI 直达 window from the SPA top-nav button (personal
	// edition). Publishes a bus signal the desktop shell's SSE listener acts on.
	api.POST("/shell/open-ai-window", s.shellHandler.OpenAIWindow)

	// Comment actions (outside workspace context)
	comments := api.Group("/comments")
	{
		comments.POST("/:id/send-to-agent", s.reviewHandler.SendCommentToAgent)
	}

	// Owner-level IM Bot (shared bot / multi-project routing). A bot is owned by
	// (owner_type, owner_id) (?owner=, default personal); chats are routed to a
	// project at approval time. See
	// docs/superpowers/specs/2026-07-08-imbot-shared-bot-multi-project-routing-design.md
	{
		api.GET("/imbot/bots", s.imbotHandler.ListBots)
		api.POST("/imbot/bots", s.imbotHandler.CreateBot)
		api.PUT("/imbot/bots/:cid", s.imbotHandler.UpdateBot)
		api.DELETE("/imbot/bots/:cid", s.imbotHandler.DeleteBot)
		api.POST("/imbot/bots/:cid/test", s.imbotHandler.TestBot)
		api.GET("/imbot/pending-chats", s.imbotHandler.ListPendingChatsOwner)
		api.GET("/imbot/chats", s.imbotHandler.ListActiveChatsOwner)
		api.POST("/imbot/chats/:chatid/approve", s.imbotHandler.ApproveChatOwner)
		api.POST("/imbot/chats/:chatid/reassign", s.imbotHandler.ReassignChatOwner)
		api.DELETE("/imbot/chats/:chatid", s.imbotHandler.DeleteChatOwner)
	}

	{
		api.PUT("/workspaces/:id/codex-sandbox", apipkg.SetWorkspaceCodexSandbox(s.workspaceSvc, s.authzSvc))
	}

	// WebSocket routes — protected by auth (uses query param ?token= for WS/SSE)
	ws := s.engine.Group("/ws")
	ws.Use(auth.IdentityResolver(s.cfg, s.db))
	ws.Use(auth.Middleware(s.cfg.Auth.Enabled, s.authSecret))
	{
		runGate := apipkg.LicenseRunGate(func() bool {
			return s.licenseSvc.ReadOnly()
		})
		// Consent run gate: these WS handshakes are GET upgrades and slip past the
		// GET-allowing ConsentGuard, so gate run-class streams explicitly on
		// consent too. Read-only streams (/sse, /notify) are intentionally left
		// open so an un-consented user still receives the events that drive the
		// consent gate UI.
		consentRunGate := apipkg.ConsentRunGate(consentBlocked)
		ws.GET("/workspaces/:id/terminal", runGate, consentRunGate, s.agentHandler.Terminal)
		// Local-runner log stream (SPA consumer, read-only) + desktop reverse
		// channel (run-class: gate on license + consent like other exec streams).
		ws.GET("/workspaces/:id/local-runner/logs", s.localRunnerHandler.LogsStream)
		ws.GET("/workspaces/:id/local-runner/runner", runGate, consentRunGate, s.localRunnerHandler.RunnerChannel)
		ws.GET("/repositories/:id/terminal", runGate, consentRunGate, s.repositoryHandler.Terminal)
		ws.GET("/sse", s.agentProxyHandler.SSE)
		ws.GET("/notify", s.notifyHandler.Connect)
	}

	// Repository routes
	repositories := api.Group("/repositories")
	{
		repositories.GET("", s.repositoryHandler.List)
		repositories.POST("", s.repositoryHandler.Create)
		repositories.GET("/:id", s.repositoryHandler.Get)
		repositories.PUT("/:id", s.repositoryHandler.Update)
		repositories.DELETE("/:id", s.repositoryHandler.Delete)
		repositories.GET("/:id/detail", s.repositoryHandler.GetDetail)
		repositories.GET("/:id/branches", s.repositoryHandler.GetBranches)
		repositories.GET("/:id/stats", s.repositoryHandler.GetStats)
		repositories.GET("/:id/files", s.repositoryHandler.ListFiles)
		repositories.GET("/:id/files/content", s.repositoryHandler.GetFileContent)
		repositories.GET("/:id/commits", s.repositoryHandler.ListCommits)
		repositories.POST("/:id/branches", s.repositoryHandler.CreateBranch)
		repositories.DELETE("/:id/branches", s.repositoryHandler.DeleteBranch)
		repositories.PUT("/:id/branches/checkout", s.repositoryHandler.CheckoutBranch)
		repositories.GET("/:id/worktrees", s.repositoryHandler.ListWorktrees)
		repositories.POST("/:id/worktrees", s.repositoryHandler.CreateWorktree)
		repositories.DELETE("/:id/worktrees/:worktree_id", s.repositoryHandler.RemoveWorktree)
		repositories.GET("/:id/git/status", s.gitOpsHandler.GetRepoStatus)
		repositories.GET("/:id/graph", s.repositoryHandler.GetGraph)
		repositories.GET("/:id/branch-tree", s.repositoryHandler.GetBranchTree)
		repositories.GET("/:id/commits/:hash", s.repositoryHandler.GetCommitDetail)
		repositories.POST("/:id/commit", s.repositoryHandler.CommitAll)
		repositories.POST("/:id/discard", s.repositoryHandler.DiscardAll)
		repositories.POST("/:id/discard-file", s.repositoryHandler.DiscardFile)
	}

	// Worktree routes (dedicated WorktreeService)
	worktrees := api.Group("/worktrees")
	{
		worktrees.GET("", s.worktreeHandler.List)
		worktrees.POST("", s.worktreeHandler.Create)
		worktrees.DELETE("/:id", s.worktreeHandler.Remove)
		worktrees.GET("/:id/tree", s.worktreeHandler.GetTree)
		worktrees.GET("/:id/git/status", s.worktreeHandler.GetStatus)
	}

	// File-based agents CRUD
	agents := api.Group("/agents")
	{
		agents.GET("", s.agentFileHandler.List)
		agents.GET("/:id", s.agentFileHandler.Get)
		agents.POST("", s.agentFileHandler.Create)
		agents.PUT("/:id", s.agentFileHandler.Update)
		agents.DELETE("/:id", s.agentFileHandler.Delete)
		agents.POST("/:id/sync", s.agentFileHandler.Sync)
		agents.POST("/import", s.agentFileHandler.Import)
	}

	// Agent Registry routes
	agentReg := api.Group("/agent-registry")
	{
		agentReg.GET("/list", s.agentRegistryHandler.List)
		agentReg.GET("/:source/:name", s.agentRegistryHandler.Get)
		agentReg.POST("/clone", s.agentRegistryHandler.Clone)
		agentReg.POST("/:source/refresh", s.agentRegistryHandler.Refresh)
		agentReg.POST("/custom", s.agentRegistryHandler.CreateCustom)
		agentReg.PUT("/custom/:name", s.agentRegistryHandler.UpdateCustom)
		agentReg.DELETE("/custom/:name", s.agentRegistryHandler.DeleteCustom)
	}

	// Quick actions (global)
	quickActions := api.Group("/quick-actions")
	{
		quickActions.GET("", s.quickActionHandler.List)
		quickActions.POST("", s.quickActionHandler.Create)
		quickActions.GET("/:id", s.quickActionHandler.Get)
		quickActions.PUT("/:id", s.quickActionHandler.Update)
		quickActions.DELETE("/:id", s.quickActionHandler.Delete)
		quickActions.POST("/reorder", s.quickActionHandler.Reorder)
	}

	// AI prompt generation
	api.POST("/ai/generate-prompt", s.promptGenHandler.GeneratePrompt)
	api.POST("/ai/optimize-prompt", s.promptGenHandler.OptimizePrompt)
	api.POST("/ai/suggest-column-op", s.promptGenHandler.SuggestColumnOp)

	// Env presets
	envPresets := api.Group("/env-presets")
	{
		envPresets.GET("", s.envPresetHandler.List)
		envPresets.POST("", s.envPresetHandler.Create)
		envPresets.GET("/:id", s.envPresetHandler.Get)
		envPresets.PUT("/:id", s.envPresetHandler.Update)
		envPresets.DELETE("/:id", s.envPresetHandler.Delete)
	}

	// Env accounts (subscription-platform credentials referenced by presets)
	envAccounts := api.Group("/env-accounts")
	{
		envAccounts.GET("", s.envAccountHandler.List)
		envAccounts.POST("", s.envAccountHandler.Create)
		envAccounts.GET("/:id", s.envAccountHandler.Get)
		envAccounts.PUT("/:id", s.envAccountHandler.Update)
		envAccounts.DELETE("/:id", s.envAccountHandler.Delete)
	}

	// Env providers (unified subscription-platform configs → per-agent env)
	envProviders := api.Group("/env-providers")
	{
		envProviders.GET("", s.envProviderHandler.List)
		envProviders.POST("", s.envProviderHandler.Create)
		envProviders.GET("/:id", s.envProviderHandler.Get)
		envProviders.PUT("/:id", s.envProviderHandler.Update)
		envProviders.DELETE("/:id", s.envProviderHandler.Delete)
		envProviders.GET("/:id/env", s.envProviderHandler.Env)
	}

	// Scenes (M1 — scene-based MCP/plugin management).
	// See docs/superpowers/specs/2026-05-17-scene-based-mcp-plugin-management-design.md §9.
	scenes := api.Group("/scenes")
	{
		scenes.GET("", s.sceneHandler.List)
		scenes.POST("", s.sceneHandler.Create)
		scenes.GET("/:id", s.sceneHandler.Get)
		scenes.PUT("/:id", s.sceneHandler.Update)
		scenes.DELETE("/:id", s.sceneHandler.Delete)
		scenes.POST("/:id/fork", s.sceneHandler.Fork)
	}

	// Data dashboards (M1): saved queries + dashboards + panels + panel data.
	// Spec: docs/superpowers/specs/2026-06-04-data-integration-and-dashboard-design.md
	savedQueries := api.Group("/saved-queries")
	{
		savedQueries.GET("", s.dashboardHandler.ListSavedQueries)
		savedQueries.POST("", s.dashboardHandler.CreateSavedQuery)
		savedQueries.DELETE("/:id", s.dashboardHandler.DeleteSavedQuery)
	}
	dashboards := api.Group("/dashboards")
	{
		dashboards.GET("", s.dashboardHandler.ListDashboards)
		dashboards.POST("", s.dashboardHandler.CreateDashboard)
		// One-step pin (auto-create default dashboard). Static segment is
		// registered before the :id param routes so Gin gives it priority.
		dashboards.POST("/pin", s.dashboardHandler.Pin)
		dashboards.GET("/:id", s.dashboardHandler.GetDashboard)
		dashboards.PATCH("/:id", s.dashboardHandler.UpdateDashboard)
		dashboards.DELETE("/:id", s.dashboardHandler.DeleteDashboard)
		dashboards.GET("/:id/panels", s.dashboardHandler.ListPanels)
		dashboards.POST("/:id/panels", s.dashboardHandler.AddPanel)
		dashboards.DELETE("/:id/panels/:pid", s.dashboardHandler.DeletePanel)
		dashboards.GET("/:id/panels/:pid/data", s.dashboardHandler.PanelData)
		// Move / copy a panel to another dashboard (manual organization).
		dashboards.POST("/:id/panels/:pid/move", s.dashboardHandler.MovePanel)
		dashboards.POST("/:id/panels/:pid/copy", s.dashboardHandler.CopyPanel)
	}
	// In-workspace "data" view: panels pinned from this workspace. Same :id param
	// name as the other /workspaces/:id routes (Gin requires consistency).
	api.GET("/workspaces/:id/panels", s.dashboardHandler.ListWorkspacePanels)

	// White-box memory (owner-scoped, versioned, traceable).
	memories := api.Group("/memories")
	{
		memories.GET("", s.memoryHandler.List)
		memories.POST("", s.memoryHandler.Create)
		memories.GET("/:id", s.memoryHandler.Get)
		memories.PUT("/:id", s.memoryHandler.Update)
		memories.DELETE("/:id", s.memoryHandler.Delete)
		memories.POST("/:id/restore", s.memoryHandler.Restore)
		memories.GET("/:id/versions", s.memoryHandler.ListVersions)
		memories.POST("/:id/rollback", s.memoryHandler.Rollback)
		// Staleness review-queue resolution (keep = restore + clear flag; confirm
		// = leave soft-deleted + clear flag).
		memories.POST("/:id/keep", s.memoryHandler.KeepReviewed)
		memories.POST("/:id/confirm-delete", s.memoryHandler.ConfirmReviewedDelete)
	}

	// Per-workspace scene layer + projection routes.
	wsScenes := api.Group("/workspaces/:id")
	{
		wsScenes.GET("/scene-layers", s.workspaceSceneHandler.ListLayers)
		wsScenes.POST("/scene-layers", s.workspaceSceneHandler.Attach)
		wsScenes.PATCH("/scene-layers/:layerID", s.workspaceSceneHandler.Move)
		wsScenes.DELETE("/scene-layers/:layerID", s.workspaceSceneHandler.Detach)
		wsScenes.GET("/scene-projection", s.workspaceSceneHandler.GetProjection)
		wsScenes.POST("/scene-projection/recompute", s.workspaceSceneHandler.Recompute)
		// User-initiated plugin install. Scene Apply only records the plan;
		// this endpoint actually runs `claude plugin install` after the
		// user clicks "Install" in the SPA.
		wsScenes.POST("/scene-projection/plugins/install", s.workspaceSceneHandler.InstallPlugins)
		// Dismiss / restore a scene-declared plugin so the banner stops (or
		// resumes) surfacing its pending/failed row — the escape hatch when a
		// plugin can't or won't be installed.
		wsScenes.POST("/scene-projection/plugins/dismiss", s.workspaceSceneHandler.DismissPlugin)
		wsScenes.GET("/scene-recommendations", s.workspaceSceneHandler.Recommendations)
	}
	// Ad-hoc plugin install (chat-input "skill install" dialog). Scope can be
	// "global" (default ~/.claude/) or "workspace" (the workspace's bound
	// claude-account ConfigDir). Sits next to the scene-driven install path
	// above — the scene system manages plugins declared by scene layers, this
	// endpoint installs one plugin the user picked from a curated list.
	api.POST("/plugins/install", s.pluginInstallHandler.Install)
	api.POST("/plugins/uninstall", s.pluginInstallHandler.Uninstall)
	api.POST("/plugins/check-installed", s.pluginInstallHandler.CheckInstalled)
	api.POST("/plugins/marketplaces", s.pluginInstallHandler.AddMarketplace)
	// Cross-agent skill management console (issue #666): catalog + install/
	// enable state per agent/scope. Install != enable - installs land in the
	// niuniu store (or the claude plugin cache) disabled by default; enable
	// turns them on globally or per workspace (scenes do the latter).
	api.GET("/skills", s.skillsHandler.List)
	api.POST("/skills/install", s.skillsHandler.Install)
	api.POST("/skills/enable", s.skillsHandler.Enable)
	api.POST("/skills/disable", s.skillsHandler.Disable)
	api.POST("/skills/update", s.skillsHandler.Update)
	api.POST("/skills/uninstall", s.skillsHandler.Uninstall)
	// Per-project default-scene prefill list.
	projScenes := api.Group("/projects/:id")
	{
		projScenes.GET("/default-scenes", s.projectSceneHandler.List)
		projScenes.POST("/default-scenes", s.projectSceneHandler.Attach)
		projScenes.DELETE("/default-scenes/:sceneID", s.projectSceneHandler.Detach)
	}

	// Project blueprints / templates (owner-scoped). Save lives under
	// /projects/:id/blueprints (above); list + delete are top-level so the
	// new-project picker can list templates across the caller's owners.
	api.GET("/project-blueprints", s.projectBlueprintHandler.List)
	api.POST("/project-blueprints", s.projectBlueprintHandler.Create)
	api.GET("/project-blueprints/default", s.projectBlueprintHandler.GetDefault)
	api.GET("/project-blueprints/:id", s.projectBlueprintHandler.GetDetail)
	api.PUT("/project-blueprints/:id", s.projectBlueprintHandler.Update)
	api.DELETE("/project-blueprints/:id", s.projectBlueprintHandler.Delete)
	api.PUT("/project-blueprints/:id/default", s.projectBlueprintHandler.SetDefault)
	api.POST("/project-blueprints/:id/duplicate", s.projectBlueprintHandler.Duplicate)

	teamH := s.teamHandler

	// Workspace-scoped blackboard routes (per-workspace coordination scratchpad).
	// 团队共享空间（blackboard）属多租户组织能力，随 org 功能分级一起门控。
	wsTeam := workspaces.Group("/:id/team")
	{
		wsTeam.Use(apipkg.LicenseFeatureGate(func(f string) bool {
			return s.licenseSvc.FeatureEnabled(f)
		}, license.FeatureOrg))
		wsTeam.GET("/blackboard", teamH.ListBlackboard)
		wsTeam.POST("/blackboard", teamH.WriteBlackboard)
	}

	// Harness spec routes — order: /resolve must come before /:id
	harnessSpecs := api.Group("/harness/specs")
	{
		harnessSpecs.GET("", s.harnessHandler.ListGlobalSpecs)
		harnessSpecs.GET("/resolve", s.harnessHandler.ResolveForProject)
		harnessSpecs.GET("/:id", s.harnessHandler.GetSpec)
		harnessSpecs.POST("", s.harnessHandler.CreateSpec)
		harnessSpecs.PUT("/:id", s.harnessHandler.UpdateSpec)
		harnessSpecs.DELETE("/:id", s.harnessHandler.DeleteSpec)
		// On-demand checker invocation — body is optional. Runs the spec's
		// registered checker once with the supplied inputs and returns the
		// single CheckResult, bypassing the phase-exit pipeline.
		harnessSpecs.POST("/:id/test", s.harnessHandler.TestSpec)
	}

	// Harness gate-check route under workspace (gate system, kept)
	workspaces.POST("/:id/harness/gate-check", s.harnessHandler.RunGateCheck)

	// Harness checks route under workspace
	workspaces.GET("/:id/harness/checks", s.harnessHandler.ListChecks)

	// Pre-commit harness gate (runs all enabled specs with trigger_on='pre_commit'
	// against caller-supplied commit context, persists results, returns aggregate
	// verdict). Also reachable from agents over /mcp/* via harness_pre_commit_check.
	workspaces.POST("/:id/harness/pre-commit-check", s.harnessHandler.PreCommitCheck)

	// Swagger UI for interactive API documentation
	api.GET("/docs/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	// Keep YAML spec endpoint for programmatic access
	api.GET("/openapi.yaml", func(c *gin.Context) {
		c.File("./docs/swagger.yaml")
	})

	// Serve frontend SPA
	// All non-API requests serve static files or fallback to index.html
	// Serve frontend SPA (if embedded)
	if s.frontendFS != nil {
		s.engine.NoRoute(spaFileHandler(s.frontendFS))
	}
}

// spaFileHandler serves embedded frontend static files, falling back to the SPA
// index.html for client-side routes (so deep links reload correctly).
//
// Caveat that shaped the draw.io integration: c.FileFromFS delegates to
// http.FileServer, whose stdlib rule 301-redirects any request path ending in
// "/index.html" to "./". A request for "/drawio/index.html" therefore never
// serves that file — it redirects to "/drawio/", which this handler cannot open
// as an embedded dir (trailing slash → invalid fs path) and so falls back to the
// SPA, rendering a "Not Found" route inside the draw.io iframe. The vendored
// editor is thus embedded via a non-index "drawio.html" entry; see
// server/web/src/components/canvas/drawio-embed.ts. Covered by router_spa_test.go.
func spaFileHandler(frontendFS fs.FS) gin.HandlerFunc {
	fsHTTP := http.FS(frontendFS)
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		// fs.FS.Open requires paths without leading slash
		cleanPath := strings.TrimPrefix(path, "/")

		// Try to serve the exact file first (CSS, JS, images, etc.)
		if cleanPath != "" {
			if f, err := frontendFS.Open(cleanPath); err == nil {
				f.Close()
				c.FileFromFS(path, fsHTTP)
				return
			}
		}

		// Fallback to index.html for SPA client-side routing (root "/" or client routes)
		c.FileFromFS("/", fsHTTP)
	}
}

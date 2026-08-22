package server

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/agentproxy"
	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/consent"
	"github.com/niuniu-dev/niuniu/internal/dataconn"
	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/imageopt"
	"github.com/niuniu-dev/niuniu/internal/imbot"
	"github.com/niuniu-dev/niuniu/internal/imbot/dingtalk"
	"github.com/niuniu-dev/niuniu/internal/imbot/lark"
	"github.com/niuniu-dev/niuniu/internal/imbot/telegram"
	"github.com/niuniu-dev/niuniu/internal/imbot/wechat"
	"github.com/niuniu-dev/niuniu/internal/imbot/wework"
	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/kbindex"
	"github.com/niuniu-dev/niuniu/internal/notify"
	"github.com/niuniu-dev/niuniu/internal/registry"
	"github.com/niuniu-dev/niuniu/internal/scheduler"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/niuniu-dev/niuniu/internal/telemetry"
	"github.com/niuniu-dev/niuniu/license"
)

type Server struct {
	engine     *gin.Engine
	cfg        *config.Config
	db         *sql.DB
	queries    *store.Queries
	frontendFS fs.FS

	// telemetryReporter is the anonymous personal-edition open-event reporter
	// (Epic #329 / #365). nil on vendor-hosted team edition (Auth.Enabled=true).
	// Held so the #366 /api/config setter can flip its live opt-out.
	telemetryReporter *telemetry.Reporter

	// Services
	projectSvc           *service.ProjectService
	projectBlueprintSvc  *service.ProjectBlueprintService
	repositorySvc        *service.RepositoryService
	issueActivitySvc     *service.IssueActivityService
	issueChecklistSvc    *service.IssueChecklistService
	issueCommentSvc      *service.IssueCommentService
	kanbanSvc            *service.KanbanService
	memoryMaintBoard     service.MaintBoard
	epicExecSvc          *service.EpicExecutionService
	checkpointSvc        *service.CheckpointService
	workspaceSvc         *service.WorkspaceService
	localRunnerSvc       *service.LocalRunnerService
	worktreeSvc          *service.WorktreeService
	agentMgr             *service.AgentManager
	reviewSvc            *service.ReviewService
	gitOpsSvc            *service.GitOpsService
	directorySvc         *service.DirectoryService
	systemDepsSvc        *service.SystemDepsService
	agentProxy           *agentproxy.AgentProxy
	agentFileSvc         *service.AgentService
	quickActionSvc       *service.QuickActionService
	envPresetSvc         *service.EnvPresetService
	envAccountSvc        *service.EnvAccountService
	envProviderSvc       *service.EnvProviderService
	sceneSvc             *service.SceneService
	sceneSeeder          *service.SceneSeeder
	sceneLayerSvc        *service.SceneLayerService
	sceneProjector       *service.SceneProjector
	sceneMatcher         *service.SceneMatcher
	pluginInstaller      *service.PluginInstaller
	marketplaceManager   *service.MarketplaceManager
	skillManager         *service.SkillManager
	agentRegistry        *registry.AgentRegistry
	agentRegistryHandler *api.AgentRegistryHandler
	scheduler            *scheduler.Scheduler
	notifyHub            *notify.NotificationHub
	notifyHandler        *api.NotifyHandler
	harnessSvc           *service.HarnessService
	harnessHandler       *api.HarnessHandler
	mcpGenerator         *service.MCPConfigGenerator
	mcpRegistry          *service.ClaudeMCPRegistry
	mcpDetector          *service.WorkspaceMCPDetector
	teamHandler          *api.TeamHandler
	memorySvc            *service.MemoryService
	memoryHandler        *api.MemoryHandler
	cleanupSvc           *service.WorkspaceCleanupService
	cleanupHandler       *api.WorkspaceCleanupHandler

	// External credential management (encrypted token storage).
	intgKeyring          *crypto.Keyring
	extCredSvc           *service.ExternalCredentialService
	extCredHandler       *api.ExternalCredentialHandler
	extSourceSvc         *service.ExternalSourceService
	extSourceHandler     *api.ExternalSourceHandler
	gitRemoteCredSvc     *service.GitRemoteCredentialService
	gitRemoteCredHandler *api.GitRemoteCredentialHandler

	// External API proxy — replacement for the old L4 work-item tools
	// and all provider-specific adapters. Generic, schema-driven proxy
	// layer with auth injection + whitelist enforcement + audit logging.
	externalProviderSvc *service.ExternalProviderService
	externalProxySvc    *service.ExternalProxyService

	// Data integration (M1): owner-scoped SQL data sources + connector
	// registry/pool. Parallel to the HTTP external-proxy above; see
	// docs/superpowers/specs/2026-06-04-data-integration-and-dashboard-design.md
	dataconnRegistry     *dataconn.Registry
	dataconnPool         *dataconn.Pool
	dataSourceSvc        *service.DataSourceService
	dataSourceHandler    *api.DataSourceHandler
	knowledgeBaseHandler *api.KnowledgeBaseHandler
	dataProxySvc         *service.DataProxyService

	// Data dashboards (M1): saved queries / dashboards / panels.
	dashboardSvc     *service.DashboardService
	dashboardHandler *api.DashboardHandler

	// Knowledge base (KB base1): owner-scoped knowledge stores + FTS index.
	kbIndexMgr *kbindex.Manager
	kbSvc      *service.KBService
	kbHandler  *api.KBHandler

	// IM Bot remote channels (Epic #555): project-level channel CRUD + pairing,
	// a ConnectorManager of per-channel outbound long connections, and an
	// event-bus dispatcher pushing outbound notifications.
	imbotSvc        *service.IMBotService
	imbotHandler    *api.IMBotHandler
	imbotConnMgr    *imbot.ConnectorManager
	imbotDispatcher *service.IMBotDispatcher

	// Handlers
	projectHandler          *api.ProjectHandler
	repositoryHandler       *api.RepositoryHandler
	issueChecklistHandler   *api.IssueChecklistHandler
	issueCommentHandler     *api.IssueCommentHandler
	issueTimelineHandler    *api.IssueTimelineHandler
	kanbanHandler           *api.KanbanHandler
	assistantHandler        *api.AssistantHandler
	epicExecHandler         *api.EpicExecutionHandler
	workspaceHandler        *api.WorkspaceHandler
	tokenUsageHandler       *api.TokenUsageHandler
	workspaceMCPHandler     *api.WorkspaceMCPHandler
	localRunnerHandler      *api.LocalRunnerHandler
	worktreeHandler         *api.WorktreeHandler
	agentHandler            *api.AgentHandler
	reviewHandler           *api.ReviewHandler
	gitOpsHandler           *api.GitOpsHandler
	directoryHandler        *api.DirectoryHandler
	systemDepsHandler       *api.SystemDepsHandler
	fsHandler               *api.FSHandler
	agentProxyHandler       *api.AgentProxyHandler
	slashCommandHandler     *api.SlashCommandHandler
	workspaceOpsHandler     *api.WorkspaceOpsHandler
	checkpointHandler       *api.CheckpointHandler
	agentFileHandler        *api.AgentFileHandler
	promptGenHandler        *api.PromptGenHandler
	workspaceTaskHandler    *api.WorkspaceTaskHandler
	pinnedMessageHandler    *api.PinnedMessageHandler
	quickActionHandler      *api.QuickActionHandler
	queueHandler            *api.QueueHandler
	scheduleHandler         *api.ScheduleHandler
	healthHandler           *api.HealthHandler
	appUpdateHandler        *api.AppUpdateHandler
	configHandler           *api.ConfigHandler
	eventsHandler           *api.EventsHandler
	envPresetHandler        *api.EnvPresetHandler
	envAccountHandler        *api.EnvAccountHandler
	envProviderHandler       *api.EnvProviderHandler
	sceneHandler            *api.SceneHandler
	workspaceSceneHandler   *api.WorkspaceSceneHandler
	pluginInstallHandler    *api.PluginInstallHandler
	skillsHandler           *api.SkillsHandler
	projectSceneHandler     *api.ProjectSceneHandler
	projectBlueprintHandler *api.ProjectBlueprintHandler
	authHandler             *api.AuthHandler
	mfaHandler              *api.MFAHandler
	attachmentHandler       *api.AttachmentHandler
	fileTreeHandler         *api.FileTreeHandler
	relayHandler            *api.RelayHandler
	shellHandler            *api.ShellHandler
	autostartHandler        *api.AutostartHandler
	gitIdentitySvc          *service.GitIdentityService
	gitIdentityHandler      *api.GitIdentityHandler
	serverSettingsSvc       *service.ServerSettingsService
	adminSettingsHandler    *api.AdminSettingsHandler
	licenseSvc              license.Gate
	licenseHandler          *api.LicenseHandler
	consentSvc              *consent.Service
	consentHandler          *api.ConsentHandler
	authzSvc                *service.Authz
	eventBus                *event.Bus

	relaySvc *service.RelayService

	orgSvc     *service.OrgService
	orgHandler *api.OrgHandler
	meHandler  *api.MeHandler

	adminUserSvc     *service.AdminUserService
	adminUserHandler *api.AdminUserHandler

	userSvc      *service.UserService
	usersHandler *api.UsersHandler

	authSecret  string
	mcpSessions *service.MCPSessionService

	permService          *service.PermissionService
	mcpPermissionHandler *api.MCPPermissionHandler
	permissionHandler    *api.PermissionHandler

	askUserService    *service.AskUserService
	mcpAskUserHandler *api.MCPAskUserHandler
	askUserHandler    *api.AskUserHandler

	labelSvc     *service.LabelService
	labelHandler *api.LabelHandler
	issueHandler *api.IssueHandler
}

// slogRecovery is a recovery middleware that logs panics using slog
func slogRecovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic recovered",
					"path", c.Request.URL.Path,
					"panic", r,
					"stack", string(debug.Stack()),
				)
				c.AbortWithStatusJSON(http.StatusInternalServerError, api.ErrorResponse{
					Error: api.ErrorDetail{
						Code:    "INTERNAL_ERROR",
						Message: "internal server error",
					},
				})
			}
		}()
		c.Next()
	}
}

// defaultAdminPassword is the built-in bootstrap password shipped as the
// config default (see config.SetDefaults, auth.users). Kept in sync with that
// default; the startup guard refuses to run a network-auth-enabled server while
// it is still in effect.
const defaultAdminPassword = "niuniu123"

// redactQueryToken scrubs the value of auth-bearing query params (?token= /
// ?ws_token=) from a request path before it is logged. WS/SSE endpoints accept
// the JWT via query string, and gin's access log records the full path — without
// this the long-lived token would land in access logs, proxy logs, and any
// Referer-based trace.
func redactQueryToken(path string) string {
	i := strings.IndexByte(path, '?')
	if i < 0 {
		return path
	}
	base, raw := path[:i], path[i+1:]
	vals, err := url.ParseQuery(raw)
	if err != nil {
		return base + "?[redacted]"
	}
	redacted := false
	for _, k := range []string{"token", "ws_token"} {
		if vals.Has(k) {
			vals.Set(k, "REDACTED")
			redacted = true
		}
	}
	if !redacted {
		return path
	}
	return base + "?" + vals.Encode()
}

// redactingLogFormatter mirrors gin's default access-log line but scrubs auth
// tokens from the path (see redactQueryToken). Color codes resolve to empty
// strings in release mode / non-TTY output, matching gin's default behavior.
func redactingLogFormatter(p gin.LogFormatterParams) string {
	statusColor := p.StatusCodeColor()
	methodColor := p.MethodColor()
	resetColor := p.ResetColor()
	if p.Latency > time.Minute {
		p.Latency = p.Latency.Truncate(time.Second)
	}
	return fmt.Sprintf("[GIN] %v |%s %3d %s| %13v | %15s |%s %-7s %s %#v\n%s",
		p.TimeStamp.Format("2006/01/02 - 15:04:05"),
		statusColor, p.StatusCode, resetColor,
		p.Latency,
		p.ClientIP,
		methodColor, p.Method, resetColor,
		redactQueryToken(p.Path),
		p.ErrorMessage,
	)
}

func New(cfg *config.Config, db *sql.DB, frontendFS fs.FS) *Server {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.RedirectTrailingSlash = false
	// Trust X-Forwarded-For only from loopback + private ranges (a same-host
	// Caddy / docker-bridge reverse proxy). Gin's default trusts EVERY proxy,
	// which lets any client spoof X-Forwarded-For and defeat the per-IP login
	// rate limiter + IP lockout + audit trail. With this list, c.ClientIP()
	// falls back to the real RemoteAddr for direct public access and only honors
	// XFF injected by our trusted front proxy.
	if err := engine.SetTrustedProxies([]string{
		"127.0.0.1/8", "::1/128",
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7",
	}); err != nil {
		slog.Warn("failed to set trusted proxies; client IPs may be spoofable", "error", err)
	}
	engine.Use(gin.LoggerWithFormatter(redactingLogFormatter), slogRecovery())

	s := &Server{
		engine:     engine,
		cfg:        cfg,
		db:         db,
		queries:    store.NewQueries(db),
		frontendFS: frontendFS,
	}
	store.Migrate(db)

	authz := service.NewAuthz(s.queries, db)
	s.authzSvc = authz
	s.mcpSessions = service.NewMCPSessionService(s.queries)

	// Event bus (initialized early — needed by KanbanService and others)
	s.eventBus = event.NewBus()

	// Notification hub for WebSocket push (initialized early — needed by services)
	s.notifyHub = notify.NewNotificationHub()

	// Permission service backs /mcp/permission-prompt and the chat-side
	// REST decisions (Tasks 4-6). Default 2h timeout matches the spec.
	s.permService = service.NewPermissionService(db, s.eventBus, s.notifyHub, 2*time.Hour)
	if err := s.permService.OnStartup(context.Background()); err != nil {
		slog.Warn("permission service: startup recovery failed", "err", err)
		// non-fatal; just means stale rows remain pending in DB
	}
	s.mcpPermissionHandler = &api.MCPPermissionHandler{Perm: s.permService, Q: s.queries}
	s.permissionHandler = &api.PermissionHandler{
		DB:    store.Wrap(db),
		Perm:  s.permService,
		Authz: authz,
	}
	s.pinnedMessageHandler = &api.PinnedMessageHandler{
		DB:    store.Wrap(db),
		Authz: authz,
	}

	// Ask-user service backs /mcp/ask-user-question and the chat-side REST
	// answer endpoints. Same 2h default timeout as PermissionService.
	s.askUserService = service.NewAskUserService(db, s.eventBus, s.notifyHub, 2*time.Hour)
	if err := s.askUserService.OnStartup(context.Background()); err != nil {
		slog.Warn("ask-user service: startup recovery failed", "err", err)
	}
	s.mcpAskUserHandler = &api.MCPAskUserHandler{Svc: s.askUserService, Q: s.queries}
	s.askUserHandler = &api.AskUserHandler{
		DB:    store.Wrap(db),
		Svc:   s.askUserService,
		Authz: authz,
	}

	// Initialize services
	s.projectSvc = service.NewProjectService(s.queries, db, s.notifyHub, authz)
	s.repositorySvc = service.NewRepositoryService(s.queries, db, authz, s.cfg.DataDir)
	s.issueActivitySvc = service.NewIssueActivityService(s.queries)
	s.issueChecklistSvc = service.NewIssueChecklistService(s.queries)
	s.issueCommentSvc = service.NewIssueCommentService(s.queries)
	s.kanbanSvc = service.NewKanbanService(db, s.queries, s.issueActivitySvc, s.eventBus, s.notifyHub)
	s.workspaceSvc = service.NewWorkspaceService(s.queries, db, &s.cfg.Workspace, s.cfg.DataDir, s.notifyHub, authz)
	s.localRunnerSvc = service.NewLocalRunnerService(s.queries, store.Wrap(db), s.notifyHub)
	s.workspaceSvc.SetPermissionService(s.permService)
	s.workspaceSvc.SetAskUserService(s.askUserService)
	// Wire the event bus so archiving an incomplete Epic-child workspace emits a
	// workspace_completed{success:false} the execution engine can act on.
	s.workspaceSvc.SetEventBus(s.eventBus)
	s.worktreeSvc = service.NewWorktreeService(s.queries, &s.cfg.Workspace)
	s.agentMgr = service.NewAgentManager(s.queries, &s.cfg.Agent)
	s.agentMgr.SetNotifyHub(s.notifyHub)
	s.agentMgr.SetMCPSessionService(s.mcpSessions)
	s.agentMgr.SetPermissionService(s.permService)
	s.agentMgr.SetAskUserService(s.askUserService)
	// mcpWriter is wired after mcpGen is created below.
	s.reviewSvc = service.NewReviewService(s.queries)
	s.gitOpsSvc = service.NewGitOpsService(s.queries, s.notifyHub)
	s.directorySvc = service.NewDirectoryService(s.cfg.DataDir)
	s.systemDepsSvc = service.NewSystemDepsService()

	s.quickActionSvc = service.NewQuickActionService(s.queries, db, authz)
	// Seed the built-in studio quick actions under the user/0 sentinel
	// (issue #234). Idempotent (keyed on slug); failure is non-fatal.
	if err := s.quickActionSvc.SeedDefaults(context.Background()); err != nil {
		slog.Warn("seed quick actions failed", "error", err)
	}

	// Env preset service and handler — no more auto-seeded preset values.
	s.envPresetSvc = service.NewEnvPresetService(s.queries, db, authz)
	s.envPresetHandler = api.NewEnvPresetHandler(s.envPresetSvc)
	s.envPresetHandler.Authz = authz
	// One-shot AI helper subprocesses (goal suggest/classify, column-op suggest)
	// run outside any workspace; let them inherit the provider preset an owner
	// marked with service.OneShotProviderMarker. Resolved lazily per call.
	agentproxy.OneShotProviderEnvFunc = s.envPresetSvc.ResolveOneShotEnv
	s.envPresetHandler.DB = db

	// Env account + provider services. Seed defaults ONLY on the very first run
	// (fresh install with empty tables). Once seeded (or on an existing install
	// that already has data), mark done so user-deleted items never come back.
	s.envAccountSvc = service.NewEnvAccountService(s.queries, db, authz)
	s.envAccountHandler = api.NewEnvAccountHandler(s.envAccountSvc)
	s.envAccountHandler.Authz = authz
	s.envAccountHandler.DB = db

	s.envProviderSvc = service.NewEnvProviderService(s.queries, db, authz)
	s.envProviderHandler = api.NewEnvProviderHandler(s.envProviderSvc, s.envPresetSvc, s.envAccountSvc)
	s.envProviderHandler.Authz = authz
	s.envProviderHandler.DB = db

	if !store.HasEnvSeedRun(db) {
		existingProviders, _ := s.queries.ListEnvProviders(context.Background())
		existingAccounts, _ := s.queries.ListEnvAccounts(context.Background())
		if len(existingProviders) == 0 && len(existingAccounts) == 0 {
			// Truly fresh install — seed defaults for the first time.
			if err := s.envAccountSvc.SeedDefaults(context.Background()); err != nil {
				slog.Warn("seed env accounts failed", "error", err)
			}
			if err := s.envProviderSvc.SeedDefaults(context.Background()); err != nil {
				slog.Warn("seed env providers failed", "error", err)
			}
		}
		// Mark done regardless: existing installs (with data) skip seeding;
		// fresh installs just seeded. Either way, never re-seed again.
		store.MarkEnvSeedDone(db)
	}

	// Scene services (M1 — see docs/superpowers/specs/2026-05-17-scene-based-mcp-plugin-management-design.md).
	// Order: SceneService → SceneSeeder seeds builtins from embedded YAML →
	// PluginInstaller + SceneProjector wires depend on MCPConfigGenerator which is
	// initialized below (after agent manager) — projector/layer/matcher set after
	// mcpGenerator. SceneHandler can be constructed eagerly because it only depends
	// on SceneService + Authz.
	s.sceneSvc = service.NewSceneService(db)
	s.sceneSeeder = service.NewSceneSeeder(s.queries)
	if err := s.sceneSeeder.Run(context.Background()); err != nil {
		slog.Warn("seed builtin scenes failed", "error", err)
	}
	s.sceneHandler = api.NewSceneHandler(s.sceneSvc, authz)
	s.projectSceneHandler = api.NewProjectSceneHandler(s.queries, authz)
	s.projectBlueprintSvc = service.NewProjectBlueprintService(db, s.queries)
	if err := s.projectBlueprintSvc.SeedBuiltins(context.Background()); err != nil {
		slog.Warn("seed builtin project blueprints failed", "error", err)
	}
	// SeedBuiltins is all-or-nothing; backfill builtins added after the initial
	// set into already-seeded DBs (once, respecting later user deletion).
	if err := s.projectBlueprintSvc.BackfillOpenSpecSuperpowers(context.Background()); err != nil {
		slog.Warn("backfill openspec-superpowers blueprint failed", "error", err)
	}
	if err := s.projectBlueprintSvc.BackfillMarketingBlueprints(context.Background()); err != nil {
		slog.Warn("backfill marketing blueprints failed", "error", err)
	}
	if err := s.projectBlueprintSvc.BackfillOpsLoopBlueprints(context.Background()); err != nil {
		slog.Warn("backfill ops-loop blueprints failed", "error", err)
	}
	s.projectBlueprintHandler = api.NewProjectBlueprintHandler(s.projectBlueprintSvc, authz)

	// First-run "open and go" seed (personal / single-user edition only). A
	// fresh install ships with an empty board, which strands non-technical
	// users before they can run anything. Seed one ready-to-use office project
	// (default scene = office-doc) so an office task runs out of the box.
	// Gated to !Auth.Enabled (personal); idempotent + non-fatal. Runs after the
	// scene seeder (above) so office-doc exists for the default-scene attach.
	// Design: docs/superpowers/specs/2026-06-14-personal-local-sandbox-hardening-design.md.
	if !cfg.Auth.Enabled {
		username := cfg.Auth.SingleUser.Username
		if username == "" {
			username = "local"
		}
		var ownerID int64
		if err := store.Wrap(db).QueryRowContext(context.Background(),
			`SELECT id FROM users WHERE username = ?`, username).Scan(&ownerID); err != nil {
			slog.Warn("onboarding seed: resolve local owner failed", "username", username, "error", err)
		} else {
			seeder := service.NewOnboardingSeeder(db, s.kanbanSvc, s.projectBlueprintSvc, true,
				service.OwnerRef{Type: "user", ID: ownerID})
			if err := seeder.Run(context.Background()); err != nil {
				slog.Warn("onboarding seed failed", "error", err)
			}
		}
	}

	// Per-user git authorship (Phase 0):
	// docs/superpowers/specs/2026-05-19-per-user-git-identity-design.md
	// The service resolves users.{display_name,email,username} so PTY spawns
	// can inject GIT_AUTHOR_*/GIT_COMMITTER_* into the Claude CLI subprocess.
	s.gitIdentitySvc = service.NewGitIdentityService(db)
	s.agentMgr.SetGitIdentityService(s.gitIdentitySvc)
	s.gitIdentityHandler = api.NewGitIdentityHandler(s.gitIdentitySvc)

	// Server settings (admin-tunable global K/V). Backs the admin settings
	// endpoint and the runtime-tunable orchestration cost guardrails.
	s.serverSettingsSvc = service.NewServerSettingsService(store.Wrap(db))

	// License gate. Personal edition (auth disabled) uses a no-op Gate; the team
	// edition (auth enabled) wires the real seat/license Service. The Gate
	// interface is the seam that lets the real implementation live outside the
	// open-source core (enterprise build injects it via the same interface).
	licensePath := filepath.Join(cfg.DataDir, "license.lic")
	licenseDB := store.Wrap(db)
	if cfg.Auth.Enabled && license.Factory != nil {
		s.licenseSvc = license.Factory(license.FactoryOpts{DB: licenseDB, Path: licensePath})
		s.licenseSvc.SetSeatCounter(func(ctx context.Context) (int64, error) {
			var n int64
			err := licenseDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
			return n, err
		})
		s.licenseSvc.SetEnforced(cfg.Auth.Enabled)
		if err := s.licenseSvc.Load(context.Background()); err != nil {
			slog.Warn("license: load failed, continuing", "error", err)
		}
		s.licenseSvc.StartTicker(context.Background())
	} else {
		s.licenseSvc = license.NopGate{}
	}

	// Consent gate: per-user acceptance of the privacy & disclaimer agreement.
	// Self-contained over the same wrapped DB; the agreement TEXT lives in the
	// SPA i18n bundle, the server only tracks versions. Always enforced (the
	// disclaimer matters for every edition, including single-user personal).
	s.consentSvc = consent.NewService(store.Wrap(db))

	// AgentProxy (initialized before handlers that depend on it)
	s.agentProxy = agentproxy.NewAgentProxy(s.queries, s.cfg)
	s.agentProxy.SetServerSettings(s.serverSettingsSvc)
	// Probe claude CLI availability in a goroutine so a slow / hanging CLI never
	// blocks server startup; the result feeds ClaudeCLIAvailable(), which gates
	// the one-shot suggestion/generation helpers (goal_condition / column_op /
	// prompt-gen) and the /health probe.
	go agentproxy.ProbeClaudeCLI(context.Background())
	s.agentProxy.SetStatusHook(s.agentMgr) // wire workspace status transitions
	s.agentProxy.SetMCPSessionService(s.mcpSessions)
	// Codex approval bridge (B2): route `approval/request` notifications
	// through the same PermissionService used by Claude's MCP tool path.
	s.agentProxy.SetPermissionGate(&service.PermissionAgentProxyAdapter{Svc: s.permService})
	// Per-user git authorship for the agentproxy chat path (Phase 0).
	// GitIdentityService implicitly satisfies agentproxy.GitIdentityResolver
	// because both declare ResolveNameEmail with the same signature.
	s.agentProxy.SetGitIdentityResolver(s.gitIdentitySvc)
	mcpGen := service.NewMCPConfigGenerator(cfg)
	s.mcpRegistry = service.NewClaudeMCPRegistry()
	s.mcpDetector = service.NewWorkspaceMCPDetector(s.mcpRegistry)
	mcpGen.SetRegistry(s.mcpRegistry)
	s.agentProxy.SetMCPWriter(service.NewAgentProxyMCPWriter(mcpGen))
	s.agentMgr.SetMCPWriter(mcpGen)
	s.workspaceSvc.SetMCPGenerator(mcpGen)
	mcpGen.SetLocalRunner(s.localRunnerSvc) // conditional local-runner tool-group injection (#526 子B)
	s.mcpGenerator = mcpGen

	// Scene projector + layer service + matcher + plugin installer. These
	// depend on the MCPConfigGenerator + NotificationHub, so they're wired
	// after mcpGen is set on the struct above.
	s.pluginInstaller = service.NewPluginInstaller("")
	s.marketplaceManager = service.NewMarketplaceManager("")
	s.sceneProjector = service.NewSceneProjector(
		db,
		cfg.DataDir,
		s.mcpGenerator,
		s.pluginInstaller,
		s.notifyHub,
		// extCred is wired below once ExternalCredentialService is constructed
		// (it depends on the keyring, built later in New). The projector tolerates
		// a nil here and is back-filled via SetExternalCredentialService.
		nil,
	)
	s.sceneLayerSvc = service.NewSceneLayerService(db, s.sceneProjector)
	s.sceneMatcher = service.NewSceneMatcher(db, cfg.DataDir)
	s.workspaceSceneHandler = api.NewWorkspaceSceneHandler(
		s.sceneLayerSvc, s.sceneProjector, s.sceneMatcher, authz, s.queries,
	)
	s.pluginInstallHandler = api.NewPluginInstallHandler(
		s.pluginInstaller, s.marketplaceManager, authz, s.queries, cfg.Auth.Enabled,
	)
	// Cross-agent skill management console (issue #666, SkillsGate-style).
	s.skillManager = service.NewSkillManager(db, cfg.DataDir, s.pluginInstaller)
	s.skillsHandler = api.NewSkillsHandler(s.skillManager, authz)
	// Plumb layer service + projector into WorkspaceService so the create path
	// can install the empty base layer + prefill any project-default scenes.
	s.workspaceSvc.SetSceneLayerService(s.sceneLayerSvc)
	s.workspaceSvc.SetSceneProjector(s.sceneProjector)
	s.sceneProjector.SetLocalRunner(s.localRunnerSvc) // claude.md prompt-fragment injection (#526 子B)
	// #526: when a desktop runner connects/disconnects, regenerate that
	// workspace's .mcp.json so the local-runner tool group matches presence on
	// the NEXT agent launch (MCP config is a session-start snapshot — this fixes
	// "runner came online after the session started, tools stayed disabled").
	s.localRunnerSvc.SetReproject(func(ctx context.Context, wsID int64) {
		if _, err := s.sceneProjector.Apply(ctx, wsID); err != nil {
			slog.Warn("local-runner: scene reprojection failed", "workspace", wsID, "error", err)
		}
	})

	// Initialize handlers
	s.projectHandler = api.NewProjectHandler(s.projectSvc, s.kanbanSvc, s.projectBlueprintSvc, authz)
	s.projectHandler.DB = db
	s.repositoryHandler = api.NewRepositoryHandler(s.repositorySvc)
	s.repositoryHandler.Authz = authz
	s.repositoryHandler.DB = db
	s.issueChecklistHandler = api.NewIssueChecklistHandler(s.issueChecklistSvc)
	s.issueChecklistHandler.Authz = authz
	s.issueCommentHandler = api.NewIssueCommentHandler(s.issueCommentSvc)
	s.issueCommentHandler.Authz = authz
	// Per-issue execution timeline recorder (spec §23.7). Shared across the floor
	// gate, advance/abandon path, ask_user, and the timeline read endpoint below.
	execEventSvc := service.NewExecEventService(db)
	s.issueTimelineHandler = api.NewIssueTimelineHandler(s.issueCommentSvc, s.issueActivitySvc, execEventSvc)
	s.issueTimelineHandler.Authz = authz
	s.kanbanHandler = api.NewKanbanHandler(s.kanbanSvc, s.issueChecklistSvc)
	s.kanbanHandler.Authz = authz

	// Conversational office assistant (#388): one-sentence → issue + no-repo
	// workspace + goal_condition, all over the existing kanban/workspace services.
	s.assistantHandler = api.NewAssistantHandler(s.kanbanSvc, s.workspaceSvc, s.projectSvc, authz, s.queries, s.agentProxy, service.NewAssistantRouter(), s.projectBlueprintSvc)

	// Label service + handler (Task 10) and issue subresource handler (Task 12).
	// LabelService takes the raw *sql.DB (it wraps once internally); the issue
	// handler shares the kanban service for SetAssignees/SetLabels.
	s.labelSvc = service.NewLabelService(db, authz)
	s.labelHandler = api.NewLabelHandler(s.labelSvc, authz)
	s.issueHandler = api.NewIssueHandler(s.kanbanSvc, authz)
	s.workspaceHandler = api.NewWorkspaceHandler(s.workspaceSvc)
	s.tokenUsageHandler = &api.TokenUsageHandler{
		Svc:   service.NewTokenUsageService(s.queries),
		Authz: authz,
		DB:    db,
	}
	s.workspaceHandler.Proxy = s.agentProxy
	s.workspaceHandler.AgentMgr = s.agentMgr
	s.workspaceHandler.Q = s.queries
	s.workspaceHandler.DB = db
	s.workspaceHandler.Authz = authz
	s.workspaceHandler.RepoSvc = s.repositorySvc
	s.workspaceHandler.Perm = s.permService
	s.workspaceMCPHandler = api.NewWorkspaceMCPHandler(
		s.workspaceSvc,
		s.mcpRegistry, s.mcpDetector,
		authz,
	)
	s.localRunnerHandler = api.NewLocalRunnerHandler(s.localRunnerSvc, authz)
	// Inbox service backs the /mcp/inbox/{send,read} endpoints. Writes per-
	// recipient JSON under <DataDir>/inbox/<workspace_id>/ — the on-disk shape
	// matches what the legacy niuniu-mcp inbox_* tools produced, so migrating
	// between the two is non-breaking.
	inboxRoot := filepath.Join(cfg.DataDir, "inbox")
	inboxSvc := service.NewInboxService(inboxRoot, s.queries, s.eventBus)
	s.teamHandler = &api.TeamHandler{Q: s.queries, WorkspaceSvc: s.workspaceSvc, EventBus: s.eventBus, InboxSvc: inboxSvc, Authz: authz}

	// Hourly MCP session-token maintenance. A workspace can live for weeks with
	// idle 停留 gaps between activity; the fixed TTL alone would let its token
	// expire mid-life and 401 every /mcp call. So each tick FIRST renews tokens
	// for every workspace with a live agent (PTY process or proxy session) — the
	// in-memory maps are the authoritative liveness signal, since a proxy session
	// stays alive across per-turn subprocess spawns that flip agent_status to
	// "idle". A clean Stop revokes the token, so a stopped workspace drops out of
	// renewal and its token then lapses. THEN cleanup deletes rows that have truly
	// expired (stopped/crashed sessions), preventing unbounded growth. Renew
	// before cleanup so a live token is never briefly deletable.
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		renewActive := func() {
			ctx := context.Background()
			seen := make(map[int64]bool)
			renew := func(ids []int64) {
				for _, wsID := range ids {
					if seen[wsID] {
						continue
					}
					seen[wsID] = true
					if err := s.mcpSessions.RenewForWorkspace(ctx, wsID); err != nil {
						slog.Warn("mcp session heartbeat renew failed", "workspaceID", wsID, "err", err)
					}
				}
			}
			if s.agentMgr != nil {
				renew(s.agentMgr.LiveWorkspaceIDs())
			}
			if s.agentProxy != nil {
				renew(s.agentProxy.LiveWorkspaceIDs())
			}
		}
		for range ticker.C {
			renewActive()
			if err := s.mcpSessions.CleanupExpired(context.Background()); err != nil {
				slog.Warn("mcp session cleanup failed", "err", err)
			}
		}
	}()

	// Daily maintenance: prune token-usage hourly buckets older than one year,
	// and sweep any orphaned messages/schedules left by deleted workspaces.
	// Runs once immediately on boot, then every 24h. A recover keeps a stray
	// panic from permanently killing the prune loop.
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		prune := func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("daily maintenance panicked", "recover", r)
				}
			}()
			ctx := context.Background()
			cutoff := time.Now().UTC().Add(-365 * 24 * time.Hour)
			if err := s.queries.PruneWorkspaceTokenHourly(ctx, cutoff); err != nil {
				slog.Warn("token hourly prune failed", "err", err)
			}
			if err := s.queries.PruneOrphanedAgentMessages(ctx); err != nil {
				slog.Warn("orphaned agent_messages prune failed", "err", err)
			}
			if err := s.queries.PruneOrphanedWorkspaceSchedules(ctx); err != nil {
				slog.Warn("orphaned workspace_schedules prune failed", "err", err)
			}
		}
		for {
			prune()
			<-ticker.C
		}
	}()

	// SQLite-only: WAL checkpoint + VACUUM (runs without stopping the server).
	// Starts 10 minutes after boot to avoid startup contention.
	if store.Driver == "sqlite" {
		go runSQLiteMaintenance(context.Background(), db, s.queries, s.cfg.Storage.SQLite.AgentMessageRetentionDays)
	}

	// Anonymous personal-edition telemetry (Epic #329 / #365): one delayed open
	// ping then a 24h keep-alive heartbeat. Only runs on Auth.Enabled=false
	// (personal / self-hosted single-user); nil on vendor-hosted team edition.
	// Each tick it reads a live opt-out mirror flipped by the #366 /api/config
	// setter (wired below). Best-effort, never blocks boot.
	s.telemetryReporter = telemetry.MaybeStart(context.Background(), cfg, api.Version)

	// Unified white-box memory (#256): session extraction + the project-memory
	// context file are memory operations; there is no separate learnings layer.
	s.memorySvc = service.NewMemoryService(s.queries, db, s.cfg.Agent.ClaudeCode.Command)
	// Inject the bound Claude account's CLAUDE_CONFIG_DIR (and workspace env)
	// into the extraction subprocess so it authenticates like the agent session;
	// otherwise a bare `claude -p` fails with "exit status 1" when credentials
	// live in a niuniu-managed config dir rather than ~/.claude.
	s.memoryHandler = api.NewMemoryHandler(s.memorySvc, authz, db, s.notifyHub)
	s.agentProxy.SetMemoryFileWriter(s.memorySvc)

	// Per-project workspace auto-cleanup: an hourly sweeper deletes completed /
	// not-started workspaces (and their issue) idle past the project's retention
	// window. OFF by default (projects.cleanup_enabled=0); opt in via settings.
	s.cleanupSvc = service.NewWorkspaceCleanupService(s.queries, db, s.workspaceSvc, s.kanbanSvc)
	s.cleanupHandler = api.NewWorkspaceCleanupHandler(s.cleanupSvc, authz)
	s.worktreeHandler = api.NewWorktreeHandler(s.worktreeSvc)
	s.agentHandler = api.NewAgentHandler(s.agentMgr, s.workspaceSvc)
	s.agentHandler.Authz = authz
	// Route review comments through the agentproxy chat session (not the PTY
	// AgentManager, which is idle during proxy-based chat) so "send to agent"
	// works while reviewing in focus mode.
	s.reviewSvc.SetAgentProxy(service.NewProxyShim(s.agentProxy))
	s.reviewHandler = api.NewReviewHandler(s.reviewSvc, s.workspaceSvc)
	s.reviewHandler.Authz = authz
	s.gitOpsHandler = api.NewGitOpsHandler(s.gitOpsSvc)
	s.gitOpsHandler.Authz = authz
	s.directoryHandler = api.NewDirectoryHandler(s.directorySvc)
	s.systemDepsHandler = api.NewSystemDepsHandler(s.systemDepsSvc, !s.cfg.Auth.Enabled)
	// Local-directory browser for the assistant's knowledge-base picker —
	// personal edition only (server runs on the user's own machine).
	s.fsHandler = api.NewFSHandler(!s.cfg.Auth.Enabled)
	s.agentProxyHandler = api.NewAgentProxyHandler(s.agentProxy, s.workspaceSvc)
	s.agentProxyHandler.Authz = authz
	s.quickActionHandler = api.NewQuickActionHandler(s.quickActionSvc)
	s.quickActionHandler.Authz = authz
	s.quickActionHandler.DB = db

	// Autohost 安全网: hidden-ref checkpoint service (refs/niuniu/<ws>/<issue>/<step>).
	// Shared by advance_issue (implement-column baseline snapshot), the floor gate
	// (checkpoint-on-pass + revert-to-last-passing on auto failure), and the
	// timeline/diff/revert query surface. See service/checkpoint.go.
	s.checkpointSvc = service.NewCheckpointService(store.Wrap(db), s.queries)

	// Workspace operations service and handler
	workspaceOpsSvc := service.NewWorkspaceOpsService(s.queries, s.workspaceSvc, s.kanbanSvc, s.eventBus)
	workspaceOpsSvc.SetExecEventService(execEventSvc)
	workspaceOpsSvc.SetCheckpointService(s.checkpointSvc)
	s.workspaceOpsHandler = api.NewWorkspaceOpsHandler(workspaceOpsSvc, s.workspaceSvc)
	s.workspaceOpsHandler.Authz = authz

	// Executable Epic execution engine (E4, mode A, event-driven). Subscribes
	// to workspace_completed events to advance waves. WorkspaceCreator is the
	// real WorkspaceService.
	s.epicExecSvc = service.NewEpicExecutionService(db, s.queries, s.kanbanSvc, s.workspaceSvc, s.eventBus)
	// Wire the agent proxy so each dispatched child workspace is kicked off with
	// the child issue's title + description (agent starts working autonomously).
	s.epicExecSvc.SetAgentProxy(service.NewProxyShim(s.agentProxy))
	// Wire the completer so an Epic-managed workspace whose agent finishes
	// (terminal autohost done) is auto-marked done and the epic advances.
	s.epicExecSvc.SetCompleter(workspaceOpsSvc)
	// Wire the merger so a completed CHILD workspace's worktree(s) are committed
	// and merged into the epic feature branch (epic/<id>) before the child is
	// marked done, so later waves build on the integrated result.
	s.epicExecSvc.SetMerger(workspaceOpsSvc)
	// Wire the review confirmer (spec §22.7/§23.1): an epic's review finishing asks the
	// user for one confirmation (产出 vs goal_condition 对账) before finalizing; only
	// the epic is gated — children silently complete and roll up to this review.
	s.epicExecSvc.SetReviewConfirmer(&epicReviewConfirmer{askUser: s.askUserService})
	// Auto-link any newly created workspace by its issue type (Epic -> orchestration
	// workspace; child -> kicked off). Triggered for both manual and automatic
	// creation since both go through WorkspaceService.Create.
	s.workspaceSvc.SetWorkspaceCreatedHook(s.epicExecSvc.OnWorkspaceCreated)
	// Wire the goal_condition suggester so ensureWorkspace can infer a goal for a
	// brand-new standalone workspace (advance_issue into an `instruct` column),
	// reusing the haiku suggest chain the AI-suggest endpoint uses.
	s.epicExecSvc.SetGoalSuggester(goalConditionSuggester{})
	// Routing livelock caps for advance_issue (spec §23.2): per-issue total-step and
	// per-column re-entry limits, overridable via env (default otherwise).
	s.epicExecSvc.SetRoutingLimitsFromEnv()
	// Execution-timeline recording (spec §23.7) for advance/abandon/terminal moves,
	// and the ask_user waiting-user-input projection (spec §19): the ask_user service
	// flips the linked issue's exec_status through the epic engine.
	s.epicExecSvc.SetExecEventService(execEventSvc)
	// Autohost 安全网: snapshot the worktree baseline when an issue enters the
	// implement column (advance_issue instruct branch).
	s.epicExecSvc.SetCheckpointService(s.checkpointSvc)
	s.askUserService.SetExecEventService(execEventSvc)
	s.askUserService.SetIssueExecProjector(s.epicExecSvc.ProjectIssueExecForWorkspace)
	// Orchestration cost guardrails (spec 2026-06-06 section 16): bound fan-out,
	// per-owner concurrent workspaces (queue over the cap), and per-chain cost.
	// The guard drains its queue back through the engine (StartQueuedWorkspace),
	// so wire the engine as the guard's starter and the guard onto the engine.
	orchGuard := service.NewOrchestrationGuard(s.queries, service.OrchestrationLimits{
		MaxBatchIssues:          s.cfg.Orchestration.MaxBatchIssues,
		MaxConcurrentWorkspaces: s.cfg.Orchestration.MaxConcurrentWorkspaces,
		ChainCostBudgetUSD:      s.cfg.Orchestration.ChainCostBudgetUSD,
		ChainCostWarnRatio:      s.cfg.Orchestration.ChainCostWarnRatio,
	})
	// Runtime-tunable limits (spec 2026-06-08): config values become the seeded
	// defaults; the server_settings store is the live source thereafter, so the
	// settings page can change limits without a restart.
	defBudget := int(s.cfg.Orchestration.ChainCostBudgetUSD)
	defWarnPct := int(math.Round(s.cfg.Orchestration.ChainCostWarnRatio * 100))
	orchSettings := service.NewOrchestrationSettings(
		s.serverSettingsSvc, defBudget,
		s.cfg.Orchestration.MaxConcurrentWorkspaces,
		s.cfg.Orchestration.MaxBatchIssues, defWarnPct,
	)
	seedCtx := context.Background()
	_ = s.serverSettingsSvc.SeedIfAbsent(seedCtx, service.KeyOrchBudgetUSD, strconv.Itoa(defBudget))
	_ = s.serverSettingsSvc.SeedIfAbsent(seedCtx, service.KeyOrchMaxConcurrent, strconv.Itoa(s.cfg.Orchestration.MaxConcurrentWorkspaces))
	_ = s.serverSettingsSvc.SeedIfAbsent(seedCtx, service.KeyOrchMaxBatch, strconv.Itoa(s.cfg.Orchestration.MaxBatchIssues))
	_ = s.serverSettingsSvc.SeedIfAbsent(seedCtx, service.KeyOrchWarnPct, strconv.Itoa(defWarnPct))
	orchGuard.SetSettings(orchSettings)
	orchGuard.SetStarter(s.epicExecSvc)
	s.epicExecSvc.SetGuard(orchGuard)
	s.kanbanSvc.SetMaxBatchIssues(s.cfg.Orchestration.MaxBatchIssues)
	s.kanbanSvc.SetOrchestrationSettings(orchSettings)
	s.epicExecSvc.Start()
	s.epicExecHandler = api.NewEpicExecutionHandler(s.epicExecSvc, s.kanbanSvc)
	s.epicExecHandler.Authz = authz
	// Autohost 安全网: checkpoint timeline / diff / revert handler (REST + MCP).
	s.checkpointHandler = api.NewCheckpointHandler(s.checkpointSvc, s.kanbanSvc, s.queries)
	s.checkpointHandler.Authz = authz
	// 建 issue 即起编排 (spec §13 stage 8): the creator path (CreateIssue) auto-starts
	// orchestration when a card lands directly in an `instruct` column. epicExecSvc is
	// fully wired by here (Start() called above).
	s.kanbanHandler.Orchestrator = s.epicExecSvc

	// Slash command service and handler
	slashCommandSvc := service.NewSlashCommandService(&s.cfg.Agent)
	s.slashCommandHandler = api.NewSlashCommandHandler(slashCommandSvc, authz)

	// Claude usage service and handler. JSONL-aggregation backend, no
	// Anthropic API calls. See spec docs/superpowers/specs/2026-05-02-claude-usage-jsonl-aggregation-design.md.
	claudeUsageSvc := service.NewClaudeUsageService(s.cfg.Auth.Enabled)
	// Forward Claude CLI rate_limit_event observations from the agent stream
	// into the usage panel so it can show the authoritative 5h-window reset
	// time (only obtainable via this passive path; /api/oauth/usage is blocked).
	s.agentProxy.SetUsageRecorder(claudeUsageSvc)

	// Session-state recorder: snapshots workspace git state on idle and
	// reports drift on the next --resume. See P1.1 design notes.
	s.agentProxy.SetSessionStateRecorder(service.NewSessionStateService(s.queries))

	// Agent file service
	s.agentFileSvc = service.NewAgentService(s.queries, s.cfg, authz)
	s.agentFileSvc.EnsureAgentDir()
	if err := s.agentFileSvc.MigrateAgentLayout(context.Background()); err != nil {
		slog.Warn("migrate agent layout failed", "error", err)
	}
	if err := s.agentFileSvc.SeedDefaultAgents(context.Background()); err != nil {
		slog.Warn("seed default agents failed", "error", err)
	}
	s.agentFileHandler = api.NewAgentFileHandler(s.agentFileSvc)
	s.agentFileHandler.Authz = authz

	// Prompt generation service and handler
	anthropicKey := cfg.AI.AnthropicAPIKey
	if anthropicKey == "" {
		anthropicKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	promptGenSvc := service.NewPromptGenService(anthropicKey)
	s.promptGenHandler = &api.PromptGenHandler{Svc: promptGenSvc}

	// Agent Registry
	localSource := registry.NewCLISource(cfg.Agent.ClaudeCode.Command)
	communitySource := registry.NewCommunitySource("community-default", "", false)
	if len(cfg.AgentRegistry.Registries) > 0 {
		r := cfg.AgentRegistry.Registries[0]
		communitySource = registry.NewCommunitySource(r.Name, r.URL, r.Enabled)
	}
	customSource := registry.NewCustomSource(cfg.AgentRegistry.CustomAgentsDir)
	curatedSource := registry.NewCuratedSource()
	s.agentRegistry = registry.NewAgentRegistry(localSource, communitySource, customSource, curatedSource)
	s.agentRegistryHandler = api.NewAgentRegistryHandler(s.agentRegistry)

	// Workspace task handler
	s.workspaceTaskHandler = &api.WorkspaceTaskHandler{
		Q:     s.queries,
		Proxy: s.agentProxy,
	}

	// Auth service and handler
	authSecret := config.GetAuthSecret()
	s.authSecret = authSecret
	authSvc := service.NewAuthService(s.queries, s.db, authSecret, cfg.Auth.TokenExpiry, cfg.Auth.RefreshExpiry)
	s.authHandler = api.NewAuthHandler(authSvc)
	s.mfaHandler = api.NewMFAHandler(authSvc)

	// Background GC: trim login_attempts older than 90d once per 24h.
	// Uses context.Background() per the project convention (see scheduler /
	// gateRunner). Disabled when Auth.Enabled=false because personal mode
	// produces no meaningful audit traffic.
	if cfg.Auth.Enabled {
		authSvc.Audit().StartGC(context.Background(), 90*24*time.Hour, 24*time.Hour)
		// MFA keyring: separate key file so it can be rotated independently
		// from the integration-secret key. Auto-generates on first boot.
		// MFA keyring: separate file so it can be rotated independently.
		// Auto-generated on first boot. Failure is non-fatal (team stays up
		// without MFA) and logged at ERROR level so ops notices.
		if mfaKeyring, err := crypto.LoadOrCreate(filepath.Join(cfg.DataDir, "auth_mfa_key")); err != nil {
			slog.Error("mfa keyring unavailable — MFA disabled", "error", err)
		} else {
			authSvc.MFA = service.NewMFAService(s.queries, mfaKeyring)
		}
	}

	// Refuse to boot a network-auth-enabled server that still ships the built-in
	// default admin credential (username "niuniu" / password "niuniu123"). It is
	// public knowledge, so any exposed instance would be one login away from full
	// admin takeover. Operators set real credentials in config.yaml; local/demo
	// runs that intentionally want the default can set NIUNIU_ALLOW_DEFAULT_ADMIN=1.
	if cfg.Auth.Enabled && os.Getenv("NIUNIU_ALLOW_DEFAULT_ADMIN") == "" {
		for _, u := range cfg.Auth.Users {
			if u.Password == defaultAdminPassword {
				slog.Error("refusing to start: built-in default admin password is still in effect",
					"username", u.Username,
					"fix", "set a real password in config.yaml (auth.users), or export NIUNIU_ALLOW_DEFAULT_ADMIN=1 for local/demo use")
				os.Exit(1)
			}
		}
	}

	// Seed configured users from config.yaml
	if len(cfg.Auth.Users) > 0 {
		type seedUser = struct{ Username, Password, DisplayName, Role string }
		seeds := make([]seedUser, len(cfg.Auth.Users))
		for i, u := range cfg.Auth.Users {
			seeds[i] = seedUser{u.Username, u.Password, u.DisplayName, u.Role}
		}
		if err := authSvc.SeedUsers(context.Background(), seeds); err != nil {
			slog.Warn("seed auth users failed", "error", err)
		}
	}

	// Attachment handler
	s.attachmentHandler = api.NewAttachmentHandler(s.queries, s.workspaceSvc)
	s.attachmentHandler.Authz = authz
	s.attachmentHandler.SetImageOpts(cfg.ImageOptimization.Enabled, imageopt.Options{
		TriggerLongEdgePx: cfg.ImageOptimization.TriggerLongEdgePx,
		TriggerSizeBytes:  cfg.ImageOptimization.TriggerSizeBytes,
		TargetLongEdgePx:  cfg.ImageOptimization.TargetLongEdgePx,
		TargetMaxBytes:    cfg.ImageOptimization.TargetMaxBytes,
		MinQuality:        cfg.ImageOptimization.MinQuality,
		MinLongEdgePx:     cfg.ImageOptimization.MinLongEdgePx,
	})

	// File tree handler
	s.fileTreeHandler = api.NewFileTreeHandler(s.queries)
	s.fileTreeHandler.Authz = authz

	// Harness service, pipeline runner, and handler
	s.harnessSvc = service.NewHarnessService(s.queries, authz)
	if err := s.harnessSvc.SeedDefaults(context.Background()); err != nil {
		slog.Warn("seed harness defaults failed", "error", err)
	}
	// 阶段8 §23.6: mark the build/test-class default floor specs as code_probe_only so
	// the floor gate auto-N/As them for a no-code-diff (doc/research) issue. Runs every
	// boot after SeedDefaults (idempotent UPDATE by category+name) so it covers both a
	// fresh install (specs just seeded) and an upgrade. code_probe_only is migrate-only
	// (not in sqlc), so this is anchored raw SQL on the driver-aware *store.DB.
	// harness_specs is a single GLOBAL library (no owner_type/owner_id columns —
	// see owner_schema.go), so the UPDATE must not filter on owner columns.
	if _, err := store.Wrap(db).ExecContext(context.Background(),
		`UPDATE harness_specs SET code_probe_only = 1
		 WHERE category = 'quality'
		   AND name IN ('build-test-pass', 'test-coverage')`); err != nil {
		slog.Warn("mark code-class floor specs failed", "error", err)
	}
	s.workspaceSvc.SetHarnessService(s.harnessSvc)
	s.workspaceSvc.SetRepositoryService(s.repositorySvc)
	s.workspaceSvc.SetAgentProxy(s.agentProxy)

	// GateSpecExecutor adapter: wraps harness.CheckRunner so the column-native
	// gates (exit gate / floor gate) can call the existing spec infrastructure.
	gateExec := service.NewCheckRunnerExec(s.harnessSvc.CheckRunner(), s.queries)

	// Column-native floor gate (stage 4, spec §22): run the project's
	// applicability='always' specs at the single completion choke point
	// (RequestWorkspaceCompletion). Reuses the same spec executor as the template
	// gate; raw db for the applicability + floor_retry_count reads (not in sqlc).
	// FloorRetryKicker (autohost re-engage on auto failure) is left nil for now —
	// auto floor-gate failures escalate to attention instead of self-fixing, which
	// is still deadlock-free (§22.3). RecoverFloorGates收口 any gate left mid-flight
	// by a crash before serving begins.
	workspaceOpsSvc.SetFloorGateDeps(db, gateExec, nil, 0)
	workspaceOpsSvc.RecoverFloorGates(context.Background())

	// RunService: the column-move main road (spec §4.2). Event bus is the
	// project's hub so board moves emit via SSE for the SPA.
	runSvc := service.NewRunService(db, s.queries, s.eventBus)

	// Wire RunService into KanbanService so MoveIssue delegates to
	// MoveIssueRunAware (the keystone column-move path).
	s.kanbanSvc.WithRunService(runSvc)
	// Wire epicExecSvc as the drag-into-instruct dispatcher: a human drag that
	// lands an issue in an instruct-primitive column auto-starts the workspace
	// and sends the column instruction (spec §3/§6 stage 3).
	runSvc.WithInstructDispatcher(s.epicExecSvc)
	// Wire the column-native exit gate: leaving a column that binds if_routed gate
	// specs runs them against the issue's workspace (issue-bound, reuses floor_gate).
	runSvc.WithExitGateDispatcher(workspaceOpsSvc)
	// Wire the issue-activity recorder so every cross-column move (human drag or
	// AI advance_issue / abandon_issue) lands a "moved" entry on the issue timeline.
	runSvc.WithActivityService(s.issueActivitySvc)

	// Automatic project-memory maintenance: an hourly scheduler runs, per project
	// whose schedule is due, an agent task that creates a "记忆库整理" issue, moves it
	// into the project's executor column (auto-running a workspace that fixes the
	// memory library via niuniu-mcp), then logs the run and cleans up. The feature
	// is OFF by default (empty projects.memory_sweep_cron); users opt in per project
	// or trigger a single run from project settings. Wired here so the board's
	// run-aware MoveIssue (set just above) is in place. Repo-less projects are
	// skipped (memory_orchestrator.go).
	s.memoryMaintBoard = service.NewMemoryMaintBoard(s.kanbanSvc, s.workspaceSvc, s.queries, s.epicExecSvc)
	s.memorySvc.StartMemoryMaintenanceScheduler(context.Background(), s.memoryMaintBoard)
	s.memoryHandler.WithMaintBoard(s.memoryMaintBoard) // enables the manual "run once" trigger

	// Start the per-project workspace auto-cleanup sweeper (see construction above).
	s.cleanupSvc.StartCleanupScheduler(context.Background())
	s.harnessHandler = &api.HarnessHandler{
		Svc:          s.harnessSvc,
		Registry:     s.agentRegistry,
		Proxy:        s.agentProxy,
		Q:            s.queries,
		DB:           db,
		WorkspaceSvc: s.workspaceSvc,
		MCPWriter:    s.mcpGenerator,
		Authz:        authz,
	}

	// Health handler
	s.healthHandler = api.NewHealthHandler(cfg.Server.ID, cfg.Auth.Enabled)

	// App-update handler — proxies the latest desktop release from the official
	// website changelog (api.github.com is 403-blocked from mainland China).
	s.appUpdateHandler = api.NewAppUpdateHandler("")

	// Config handler (GET/PUT /api/config). Owns the live cfg + config.Save.
	s.configHandler = api.NewConfigHandler(cfg)
	// Back the read-only capability flags in the /config snapshot (e.g.
	// assistant_enabled) with the admin-settings store.
	s.configHandler.Settings = s.serverSettingsSvc
	// Wire the telemetry opt-out toggle to the running reporter so flipping it in
	// Settings stops/resumes reporting immediately (next tick) without a restart
	// (#366 -> #365). nil reporter (team edition) leaves the toggle persist-only.
	if s.telemetryReporter != nil {
		s.configHandler.OnTelemetryToggle = s.telemetryReporter.SetEnabled
	}

	// Admin settings handler (Phase 2). Exposes the whitelisted admin-tunable
	// server settings keys (orchestration guardrails, etc.).
	s.adminSettingsHandler = api.NewAdminSettingsHandler(s.serverSettingsSvc, orchGuard)
	s.licenseHandler = api.NewLicenseHandler(s.licenseSvc)
	s.consentHandler = api.NewConsentHandler(s.consentSvc)

	// Events handler (eventBus already initialized above)
	s.eventsHandler = api.NewEventsHandler(s.eventBus)
	s.eventsHandler.Authz = authz
	s.agentProxy.SetEventBus(s.eventBus)

	// Bridge permission / ask-user events from the event bus to the SessionHub
	// so the frontend's /ws/sse channel receives them in real-time. Without this
	// bridge, the agent-sse-store never sees permission_request/permission_decided
	// events, and the in-chat permission prompt cards only appear on page refresh
	// (when the REST load() fallback in ChatPanel's useEffect runs).
	permCh := s.eventBus.Subscribe()
	proxyHub := s.agentProxy.GetHub()
	go func() {
		defer s.eventBus.Unsubscribe(permCh)
		for evt := range permCh {
			switch evt.Type {
			case event.EventPermissionRequest, event.EventPermissionDecided,
				event.EventAskUserRequest, event.EventAskUserDecided:
				proxyHub.Broadcast(evt.WorkspaceId, evt)
			}
		}
	}()

	// Notify handler (WebSocket push notifications)
	s.notifyHandler = api.NewNotifyHandler(s.notifyHub)
	s.notifyHandler.Authz = authz

	// Wire notifyHub into services that broadcast
	s.agentProxy.SetNotifyHub(s.notifyHub)
	// Wire workspace alert resolver so agent_done events carry should_alert_user_ids.
	// MUST be called before any agent session starts (i.e., before srv.Serve
	// or RecoverReviewTimers). The resolver is read without lock from session
	// goroutines; see agentproxy.AgentProxy.SetWorkspaceAlertResolver doc.
	s.agentProxy.SetWorkspaceAlertResolver(s.workspaceSvc)

	// Queue handler — broadcast via SessionHub so events reach frontend /ws/sse
	hub := s.agentProxy.GetHub()
	s.queueHandler = api.NewQueueHandler(s.queries, db, s.notifyHub, func(workspaceID int64, eventType, content string) {
		hub.Broadcast(workspaceID, event.NewOutputEvent(eventType, content, "", "system", workspaceID))
	}, s.workspaceSvc)
	s.queueHandler.Authz = authz

	// Schedule handler and scheduler service
	s.scheduleHandler = api.NewScheduleHandler(s.queries, s.workspaceSvc)
	s.scheduleHandler.Authz = authz
	s.scheduleHandler.SetDB(db)
	s.scheduler = scheduler.New(s.queries, s.agentProxy)
	s.scheduler.SetNotifyHub(s.notifyHub)
	s.scheduler.SetDB(db)
	// Pre-flight harness gate: on_schedule specs run against the workspace
	// before the agent session starts. See docs/architecture/workspace-model.md.
	s.scheduler.SetHarnessGate(s.harnessSvc)
	s.scheduler.SetLicenseGate(func() bool { return s.licenseSvc.AllowRun() })
	s.scheduleHandler.SetOnChanged(func(scheduleID int64, deleted bool) {
		s.scheduler.OnScheduleChanged(context.Background(), scheduleID, deleted)
	})
	s.scheduleHandler.SetTriggerNow(s.scheduler.TriggerNow)
	// The conversational assistant creates managed-task schedules directly
	// (via create_managed_task); wire it to the same scheduler registration
	// path so new tasks fire without a server restart.
	s.assistantHandler.SetDB(db)
	s.assistantHandler.SetScheduleChanged(func(scheduleID int64, deleted bool) {
		s.scheduler.OnScheduleChanged(context.Background(), scheduleID, deleted)
	})
	// Auto-resume on rate limit: agentproxy calls back into the scheduler to
	// create a one-shot schedule at the reset time so the workspace continues
	// its queued work on its own once the window lifts.
	s.agentProxy.SetRateLimitScheduler(s.scheduler)

	// Wire attention rollback: when agent errors, push user message back to queue
	s.agentMgr.SetQueueRollback(func(ctx context.Context, workspaceID int64) {
		session := s.agentProxy.GetSession(workspaceID)
		if session == nil {
			return
		}
		userMsgId := session.TurnUserMsgId()
		if userMsgId == "" {
			return
		}
		msg, err := s.queries.GetAgentMessage(ctx, userMsgId)
		if err != nil {
			slog.Error("rollback: get user message", "error", err, "workspaceID", workspaceID)
			return
		}
		// Idempotent check
		retryCount, err := s.queries.HasRetryItem(ctx, workspaceID)
		if err == nil && retryCount > 0 {
			return
		}
		minPos, _ := s.queries.GetMinQueuePosition(ctx, workspaceID)
		_, err = s.queries.CreateQueueItem(ctx, store.CreateQueueItemParams{
			WorkspaceID: workspaceID,
			Content:     msg.Content,
			Position:    minPos - 1000,
			Source:      "retry",
		})
		if err != nil {
			slog.Error("rollback: create queue item", "error", err, "workspaceID", workspaceID)
			return
		}
		slog.Info("rollback: pushed failed message to queue front", "workspaceID", workspaceID)
		hub.Broadcast(workspaceID, event.NewOutputEvent(event.EventQueueUpdate, "", "", "system", workspaceID))
	})

	// Recover review timers for workspaces that were in "needs_review" before restart
	s.agentMgr.RecoverReviewTimers(context.Background())

	// Recover and start scheduler
	if err := s.scheduler.RecoverOnStartup(context.Background()); err != nil {
		slog.Warn("recover scheduler failed", "error", err)
	}
	s.scheduler.Start()

	// Relay account + mobile pairing service (stays idle until StartRelay is called
	// with the real local base URL, typically right after Listen binds the addr).
	s.relaySvc = service.NewRelayService()
	s.relayHandler = api.NewRelayHandler(s.relaySvc)
	s.shellHandler = api.NewShellHandler(!s.cfg.Auth.Enabled)
	// Bus lets /shell/open-ai-window signal the desktop shell to raise the AI 直达
	// window (personal-edition top-nav button bridge).
	s.shellHandler.EventBus = s.eventBus
	// Autostart (launch-at-login) is only meaningful when niuniu-desktop
	// spawned us and passed its executable path; otherwise reports unsupported.
	s.autostartHandler = api.NewAutostartHandler(os.Getenv("NIUNIU_PERSONAL_EXE"))

	// Org service and handlers
	s.orgSvc = service.NewOrgService(s.queries, db, authz)
	s.orgSvc.SetAgentManager(s.agentMgr)      // terminate PTY sessions on member removal
	s.orgSvc.SetNotifyHub(s.notifyHub)        // close WS notification streams on member removal
	s.orgSvc.SetSSEHub(s.agentProxy.GetHub()) // close SSE streams on member removal (best-effort; see DisconnectUser TODO)
	s.orgHandler = api.NewOrgHandler(s.orgSvc)
	s.meHandler = api.NewMeHandler(s.orgSvc)

	// Admin user-resource service + handler (view/delete a user's personal
	// resources + one-click account purge). Composes the per-resource services
	// (reusing their git/worktree/dir cleanup) and OrgService (member removal).
	s.adminUserSvc = service.NewAdminUserService(
		s.queries, db, s.cfg.DataDir,
		s.projectSvc, s.workspaceSvc, s.repositorySvc, s.orgSvc, authz)
	s.adminUserSvc.SetAgentManager(s.agentMgr)      // stop the user's PTY sessions on purge
	s.adminUserSvc.SetNotifyHub(s.notifyHub)        // close WS notify streams on purge
	s.adminUserSvc.SetSSEHub(s.agentProxy.GetHub()) // close SSE streams on purge
	s.adminUserHandler = api.NewAdminUserHandler(s.adminUserSvc)

	// User service and handler
	s.userSvc = service.NewUserService(s.queries, db, authz)
	s.usersHandler = api.NewUsersHandler(s.userSvc)

	// External issue tracker integration (M1).
	// Keyring auto-generates ~/.niuniu/integration_secret on first boot;
	// failure here is fatal because we never silently fall back to plain-
	// text storage of provider tokens (spec §4.5 / §8.2).
	keyring, err := crypto.LoadOrCreate(filepath.Join(cfg.DataDir, "integration_secret"))
	if err != nil {
		panic("load integration secret: " + err.Error())
	}
	s.intgKeyring = keyring
	// Legacy registry (empty — all adapters removed). Kept for
	// ExternalCredentialService constructor compatibility; Verify now
	// returns "provider not found" which is acceptable since the new
	// proxy architecture does not need pre-flight credential checks.
	intgRegistry := integration.NewRegistry()
	s.extCredSvc = service.NewExternalCredentialService(s.queries, db, keyring, intgRegistry)
	s.extCredHandler = api.NewExternalCredentialHandler(s.extCredSvc, authz)
	// Back-fill the scene projector's credential service so scene MCP env
	// ${cred:alias.field} placeholders resolve to the owner+user decrypted value.
	if s.sceneProjector != nil {
		s.sceneProjector.SetExternalCredentialService(s.extCredSvc)
		// Refresh office-mail workspaces' config.toml when a mailbox credential
		// is changed/deleted, so no stale password snapshot lingers (spec §8 C4).
		s.extCredSvc.SetChangeHook(s.sceneProjector.ReprojectImapWorkspaces)
	}

	// Project ↔ external-tracker bindings. The UI uses List/Add/Delete to
	// register which GitHub repo / TAPD workspace each niuniu project maps
	// to; AI then reads the binding via MCP and dispatches actual API calls
	// through the external-proxy service. Browse() on the service still
	// exists but is no longer routed — adapters were removed when the
	// proxy architecture replaced them.
	s.extSourceSvc = service.NewExternalSourceService(s.queries, db, s.extCredSvc, intgRegistry, authz)
	s.extSourceHandler = api.NewExternalSourceHandler(s.extSourceSvc, authz)

	// Per-user git remote SSH credentials (v5 Phase 2 sub-phase A).
	// Spec: docs/superpowers/specs/2026-05-19-per-user-git-identity-design.md
	// Storage + management only here; PTY GIT_SSH_COMMAND materialization
	// (sub-phase B) lands in a follow-up.
	s.gitRemoteCredSvc = service.NewGitRemoteCredentialService(db, keyring)
	s.gitRemoteCredHandler = api.NewGitRemoteCredentialHandler(s.gitRemoteCredSvc)

	// External API proxy — generic, schema-driven replacement for all
	// old provider adapters and L4 work-item tools.
	providerSvc := service.NewExternalProviderService(s.queries, db)
	// Seed built-in providers (github) on every boot. Idempotent: existing
	// rows by name are skipped so user customizations stick.
	if n, err := providerSvc.SeedSystem(context.Background()); err != nil {
		slog.Warn("seed system providers", "err", err)
	} else if n > 0 {
		slog.Info("seeded system providers", "count", n)
	}
	// Release providers that used to be system-seeded (TAPD) but now ship as
	// regular user providers, so their rows become editable/deletable.
	if err := providerSvc.DemoteDeprecatedSystemProviders(context.Background()); err != nil {
		slog.Warn("demote deprecated system providers", "err", err)
	}
	proxySvc := service.NewExternalProxyService(s.queries, db, providerSvc, s.extCredSvc, authz)
	s.externalProviderSvc = providerSvc
	s.externalProxySvc = proxySvc
	// Reproject the user's office-mail workspaces when they toggle imap write-
	// permission, so config.toml (outgoing/SMTP) + permissions.deny refresh to
	// the new state (otherwise the toggle never reaches the materialized files
	// and sending stays disabled until an unrelated reprojection).
	if s.sceneProjector != nil {
		providerSvc.SetImapWritePrefChangeHook(s.sceneProjector.ReprojectImapWorkspacesForUser)
	}

	// Data integration (M1): SQL data sources. Connectors are stateless
	// today (a short-lived *sql.DB per Execute); the Pool exists so deletes
	// can Evict(id) without a later refactor. The data source config reuses
	// the same integration keyring for AES-GCM encryption.
	s.dataconnRegistry = dataconn.NewRegistry()
	s.dataconnPool = dataconn.NewPool()
	s.dataSourceSvc = service.NewDataSourceService(s.queries, db, keyring, s.dataconnRegistry)
	s.dataSourceHandler = api.NewDataSourceHandler(s.dataSourceSvc, s.dataconnPool)
	s.dataSourceHandler.Authz = s.authzSvc

	// Data proxy: the three-layer gate (read/write separation, data-scope
	// authorization, per-op confirmation via PermissionService) + audit. The
	// /mcp/data-proxy/query handler and the run_data_query MCP tool drive it.
	s.dataProxySvc = service.NewDataProxyService(s.dataSourceSvc, s.dataconnRegistry, s.permService, s.queries)

	// Data dashboards (M1): saved queries / dashboards / panels. PanelData
	// reuses the data proxy gate, so the dashboard service depends on it.
	s.dashboardSvc = service.NewDashboardService(s.queries, s.dataSourceSvc, s.dataconnRegistry, s.dataProxySvc)
	s.dashboardHandler = api.NewDashboardHandler(s.dashboardSvc, authz)

	// Knowledge base (KB base1): the full-text index lives behind kbindex.Manager
	// (per-owner SQLite sidecar, or shared Postgres tsvector/pg_trgm). On SQLite
	// the raw db arg is ignored; on Postgres it backs the shared index.
	s.kbIndexMgr = kbindex.NewManager(store.Driver, db)
	// Wire the local-source guard to the edition/config decision before any KB
	// ingest can run: personal (auth off) permits reading local paths; hosted
	// (auth on) refuses arbitrary server-file reads. Process-wide by design (the
	// download HTTP client's dial hook is a singleton reading it live).
	service.SetKBAllowLocalSources(cfg.KBAllowLocalSources())
	s.kbSvc = service.NewKBService(s.queries, cfg.DataDir, s.kbIndexMgr)
	// KB base3: MCP kb_search / kb_list surface. Owner + project are derived
	// server-side from the workspace, so visibility is owner-scoped + project-bound.
	s.kbHandler = api.NewKBHandler(s.kbSvc, db)
	// KB base4 (C ability): expose bound KB dataset dirs to the workspace agent
	// for direct read-only Read/Grep/Glob. Resolved per spawn so it tracks
	// current bindings; same owner+project visibility gate as kb_search.
	s.agentProxy.SetKBResolver(service.NewAgentProxyKBResolver(s.kbSvc))

	// Knowledge-base management UI (Epic #496 · #498): REST layer over #497's
	// KBService + raw-SQL lifecycle/binding columns. Drives the settings panel.
	s.knowledgeBaseHandler = api.NewKnowledgeBaseHandler(s.kbSvc, db, s.cfg.DataDir)
	s.knowledgeBaseHandler.Authz = s.authzSvc

	// IM Bot remote channels (Epic #555). One shared adapter registry backs both
	// the ConnectorManager (inbound long connections, W2) and the service/
	// dispatcher (outbound Push). Credentials are AES-GCM encrypted with the same
	// keyring as other server-side credentials (deploy-safe on headless Linux);
	// only the service layer decrypts, so imbot/ stays free of integration/.
	imbotAdapters := map[imbot.ChannelType]imbot.ChannelAdapter{
		imbot.ChannelLark:     lark.New(),
		imbot.ChannelTelegram: telegram.New(),
		imbot.ChannelDingTalk: dingtalk.New(),
		imbot.ChannelWework:   wework.New(),
		imbot.ChannelWechat:   wechat.New(),
	}
	s.imbotSvc = service.NewIMBotService(s.queries, db, keyring, s.authzSvc, imbotAdapters)
	// Inbound (W2): route incoming IM messages into the channel's bound project
	// via the generalized RouteInProject, deliver to the workspace agent, and
	// write permission-button decisions back through PermissionService. The
	// dispatch service reuses the shared kanban/workspace services and its own
	// classifier instance (same as the WebUI assistant).
	imbotDispatch := service.NewAssistantDispatchService(s.kanbanSvc, s.workspaceSvc, s.queries, service.NewAssistantRouter())
	s.imbotSvc.SetInbound(imbotDispatch, s.agentProxy, s.permService)
	// Let a user answer an agent's ask_user question by tapping an option button in
	// the chat (the outbound question card carries option buttons).
	s.imbotSvc.SetAskUserDecider(s.askUserService)
	s.imbotConnMgr = imbot.NewConnectorManager(s.imbotSvc, imbotAdapters, s.imbotSvc.HandleInbound)
	s.imbotSvc.SetConnectorManager(s.imbotConnMgr)
	s.imbotHandler = api.NewIMBotHandler(s.imbotSvc, s.authzSvc, s.db)
	// Wire the AI-onboarding dispatch + deliverer (T3): the same AssistantDispatchService
	// and AgentProxy that the inbound IM pipeline uses, so StartOnboarding creates a
	// real issue+workspace and delivers the kickoff prompt through the same path.
	s.imbotHandler.SetDispatch(imbotDispatch)
	s.imbotHandler.SetDeliverer(s.agentProxy)
	s.imbotDispatcher = service.NewIMBotDispatcher(s.eventBus, s.queries, s.imbotSvc, imbotAdapters)
	// Backfill credential fingerprints for legacy channels so the one-bot-per-app
	// UNIQUE constraint is enforceable (blocks a second channel for the same app);
	// leftover duplicates are logged, not deleted. Best-effort before connections start.
	s.imbotSvc.BackfillCredentialFingerprints(context.Background())
	// Establish outbound long connections for every active stream channel and
	// begin dispatching outbound notifications. Connections are process-held and
	// re-established from the store on restart.
	if err := s.imbotConnMgr.Start(context.Background()); err != nil {
		slog.Warn("imbot: connector manager start failed", "error", err)
	}
	s.imbotDispatcher.Start()

	s.setupRoutes()
	return s
}

// StartRelay starts the relay tunnel goroutine iff credentials are in the
// OS keychain. Pass the local base URL the tunnel should forward inbound
// mobile requests to (typically "http://" + listener.Addr().String()).
// Safe no-op if no credentials are saved yet.
func (s *Server) StartRelay(ctx context.Context, localBaseURL string) {
	if s.relaySvc == nil {
		return
	}
	s.relaySvc.SetLocalBaseURL(localBaseURL)
	s.relaySvc.Start(ctx)
}

// Listen binds a TCP listener at addr. Pass "127.0.0.1:0" for ephemeral port.
// Use ln.Addr() on the returned listener to discover the actual bound port.
func (s *Server) Listen(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}

// Serve runs the HTTP engine on a pre-bound listener. Blocks until error.
func (s *Server) Serve(ln net.Listener) error {
	return s.engine.RunListener(ln)
}

// Run is retained for backward compatibility (tests, any other callers).
func (s *Server) Run(addr string) error {
	ln, err := s.Listen(addr)
	if err != nil {
		return err
	}
	return s.Serve(ln)
}

// Shutdown releases background resources (hub ping loop, etc.).
// Call when the server is shutting down.
func (s *Server) Shutdown() {
	if s.relaySvc != nil {
		s.relaySvc.Stop()
	}
	if s.scheduler != nil {
		s.scheduler.Stop()
	}
	if s.imbotDispatcher != nil {
		s.imbotDispatcher.Stop()
	}
	if s.imbotConnMgr != nil {
		s.imbotConnMgr.Stop()
	}
	if s.epicExecSvc != nil {
		s.epicExecSvc.Stop()
	}
	if s.eventBus != nil {
		s.eventBus.Close()
	}
	if s.notifyHub != nil {
		s.notifyHub.Stop()
	}
	if s.agentProxy != nil {
		s.agentProxy.Stop()
	}
	if s.kbIndexMgr != nil {
		s.kbIndexMgr.Close()
	}
}

// Engine returns the Gin engine for testing purposes
func (s *Server) Engine() *gin.Engine {
	return s.engine
}

// goalConditionSuggester adapts the Claude-account-resolved haiku suggest chain to
// service.GoalConditionSuggester (used by ensureWorkspace, AI-native board stage 3).
// It resolves the caller's CLAUDE_CONFIG_DIR so the inference subprocess inherits
// the same auth niuniu's workspace agents use — the same path as the AI-suggest
// REST endpoint (api/kanban.go SuggestGoalCondition).
type goalConditionSuggester struct{}

func (g goalConditionSuggester) Suggest(ctx context.Context, userID int64, title, description string) (string, error) {
	_ = userID
	return agentproxy.SuggestGoalCondition(ctx, title, description, "")
}

func (g goalConditionSuggester) Classify(ctx context.Context, userID int64, title, description string) (*service.GoalAssessment, error) {
	_ = userID
	a, err := agentproxy.ClassifyIssueForKickoff(ctx, title, description, "")
	if err != nil {
		return nil, err
	}
	return &service.GoalAssessment{Actionable: a.Actionable, Reason: a.Reason, GoalCondition: a.GoalCondition}, nil
}

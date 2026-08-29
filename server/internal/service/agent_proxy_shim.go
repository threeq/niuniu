package service

import (
	"context"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/agentproxy"
	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/sceneenv"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// mcpWriterShim adapts *MCPConfigGenerator to agentproxy.MCPConfigWriter.
// The two interfaces share Generate's parameter list but disagree on the
// return type — service uses its own GenerateResult while agentproxy
// declares its own mirror (MCPGenerateResult) to avoid a circular
// import. This shim translates between them.
type mcpWriterShim struct{ inner *MCPConfigGenerator }

// NewAgentProxyMCPWriter wraps *MCPConfigGenerator so it satisfies the
// agentproxy.MCPConfigWriter interface. Pass the result into
// AgentProxy.SetMCPWriter.
func NewAgentProxyMCPWriter(g *MCPConfigGenerator) agentproxy.MCPConfigWriter {
	return &mcpWriterShim{inner: g}
}

func (s *mcpWriterShim) Generate(wsPath string, opts config.MCPGenerateOptions, extras []string, configDir string) (*agentproxy.MCPGenerateResult, error) {
	res, err := s.inner.Generate(wsPath, opts, extras, configDir)
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, nil
	}
	return &agentproxy.MCPGenerateResult{
		WrittenServers: res.WrittenServers,
		Unavailable:    res.Unavailable,
	}, nil
}

func (s *mcpWriterShim) GenerateClaudeSettings(wsPath string) error {
	return s.inner.GenerateClaudeSettings(wsPath)
}

func (s *mcpWriterShim) GenerateCodexConfigToml(wsPath string, opts config.MCPGenerateOptions) error {
	return s.inner.GenerateCodexConfigToml(wsPath, opts)
}

func (s *mcpWriterShim) GenerateCodexConfigArgs(opts config.MCPGenerateOptions) ([]string, error) {
	return s.inner.GenerateCodexConfigArgs(opts)
}

func (s *mcpWriterShim) SetWorkspaceKBReadonly(wsPath string, roots []string) error {
	return s.inner.SetWorkspaceKBReadonly(wsPath, roots)
}

func (s *mcpWriterShim) NiuniuMcpServer(opts config.MCPGenerateOptions) (config.McpServerEntry, error) {
	return s.inner.NiuniuMcpServer(opts)
}

// kbResolverShim adapts *KBService to agentproxy.KBDatasetResolver, translating
// the service.KBDatasetDir result into agentproxy's import-light mirror type so
// the agent-spawn path can expose bound KB dataset dirs without importing
// service (which would cycle).
type kbResolverShim struct{ inner *KBService }

// NewAgentProxyKBResolver wraps a *KBService as an agentproxy.KBDatasetResolver.
// Pass the result into AgentProxy.SetKBResolver.
func NewAgentProxyKBResolver(s *KBService) agentproxy.KBDatasetResolver {
	return &kbResolverShim{inner: s}
}

func (s *kbResolverShim) WorkspaceDatasetDirs(ctx context.Context, workspaceID int64) ([]agentproxy.KBDatasetDir, error) {
	dirs, err := s.inner.WorkspaceDatasetDirs(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]agentproxy.KBDatasetDir, 0, len(dirs))
	for _, d := range dirs {
		out = append(out, agentproxy.KBDatasetDir{
			Name:        d.Name,
			Description: d.Description,
			Root:        d.Root,
		})
	}
	return out, nil
}

// AgentProxyShim is the narrow surface services need from agentproxy. Defined
// as an interface so tests can substitute. Implemented by proxyShim wrapping
// *agentproxy.AgentProxy. Consumed by EpicExecutionService and the §18
// last-writer workspace interrupt.
type AgentProxyShim interface {
	GetOrStartSession(ctx context.Context, workspaceID, userID int64) (AgentSession, error)
	GetSession(workspaceID int64) AgentSession // nil if absent
	// PrepareUserSend opens the Enqueue gate for a genuine manual user send when
	// the workspace only LOOKS idle but a scheduled autohost resume / pending
	// wakeup is still holding the queue closed. No-op while a live loop runs.
	PrepareUserSend(ctx context.Context, workspaceID int64)
	// Deliver sends content to the workspace: queues if the session is running,
	// starts a SendLoop otherwise. Returns (queued, queueID, err).
	Deliver(ctx context.Context, workspaceID int64, workDir, content, attachments string) (bool, int64, error)
}

// AgentSession is the narrow surface services use on a session.
type AgentSession interface {
	SetActiveRunID(runID int64)
	Cancel(ctx context.Context) error // graceful stop
	SendKickoff(ctx context.Context, workspacePath, message, attachment string)
}

// proxyShim adapts *agentproxy.AgentProxy to the AgentProxyShim interface.
// Lives in service/ to avoid a circular import (agentproxy already imports
// neither service nor event directly).
type proxyShim struct{ inner *agentproxy.AgentProxy }

// NewProxyShim wraps an *agentproxy.AgentProxy as an AgentProxyShim.
func NewProxyShim(p *agentproxy.AgentProxy) AgentProxyShim {
	return &proxyShim{inner: p}
}

func (p *proxyShim) GetOrStartSession(ctx context.Context, wsID, uid int64) (AgentSession, error) {
	sess, err := p.inner.GetOrStartSession(ctx, wsID, uid)
	if err != nil {
		return nil, err
	}
	return &sessionShim{inner: sess}, nil
}

func (p *proxyShim) GetSession(wsID int64) AgentSession {
	sess := p.inner.GetSession(wsID)
	if sess == nil {
		return nil
	}
	return &sessionShim{inner: sess}
}

// sessionShim adapts *agentproxy.WorkspaceSession to the AgentSession interface.
type sessionShim struct{ inner *agentproxy.WorkspaceSession }

func (s *sessionShim) SetActiveRunID(id int64) { s.inner.SetActiveRunID(id) }
func (s *sessionShim) Cancel(ctx context.Context) error {
	return s.inner.Stop(ctx)
}
func (s *sessionShim) SendKickoff(ctx context.Context, p, msg, att string) {
	go s.inner.SendLoop(context.Background(), p, msg, att)
}

func (p *proxyShim) PrepareUserSend(ctx context.Context, workspaceID int64) {
	p.inner.PrepareUserSend(ctx, workspaceID)
}

func (p *proxyShim) Deliver(ctx context.Context, workspaceID int64, workDir, content, attachments string) (bool, int64, error) {
	return p.inner.Deliver(ctx, workspaceID, workDir, content, attachments)
}

// employeeAnalyzer is the production EmployeeAnalyzer (issue #664): it asks a
// one-shot CLI subprocess to judge a chat transcript.
//
// Crucially it runs under the TARGET PROJECT's agent configuration, not a
// hardcoded Claude: this call decides work that that project's own agent will
// then execute, so it uses the same CLI backend and the same bound provider
// credentials. An install driving Codex or Qwen against a 智谱/DeepSeek provider
// would otherwise have an employee that silently never speaks.
//
// The prompt and schema live in imbot_employee.go with the rest of the employee's
// domain logic; this shim only resolves configuration and bridges to agentproxy's
// subprocess plumbing.
type employeeAnalyzer struct {
	q *store.Queries
}

// NewEmployeeAnalyzer returns the one-shot-CLI-backed analyzer to hand to
// NewIMBotEmployee. It reports its own unavailability at call time rather than at
// construction, since CLI availability can change while the server runs.
func NewEmployeeAnalyzer(q *store.Queries) EmployeeAnalyzer { return &employeeAnalyzer{q: q} }

func (a *employeeAnalyzer) AnalyzeChat(ctx context.Context, scope EmployeeScope, transcript string) (EmployeeVerdict, error) {
	cliType, env := a.resolveAgentContext(ctx, scope)
	var v EmployeeVerdict
	if err := agentproxy.RunOneShotStructured(ctx, agentproxy.OneShotRequest{
		Prompt:     BuildEmployeeAnalysisPrompt(transcript),
		JSONSchema: EmployeeAnalysisSchema,
		CLIType:    cliType,
		Env:        env,
		// ConfigDir is left empty: niuniu is single-account per CLI (the host's
		// ~/.claude / ~/.codex login is used as-is), so there is no per-workspace
		// account dir to select. Provider-based auth arrives through Env above.
	}, &v); err != nil {
		return EmployeeVerdict{}, err
	}
	v.Action = normalizeEmployeeAction(v.Action)
	return v, nil
}

// resolveAgentContext determines which agent backend the analysis should run on
// and which provider credentials it should carry, mirroring how a real workspace
// agent for the same project would be spawned.
//
// Resolution, most specific first:
//   - a representative workspace of the project -> its cli_type and its fully
//     resolved env (sceneenv.Resolve: bound provider -> scene -> workspace env,
//     with ${ACCOUNT:*} substituted). This is the same source the agent spawn path
//     uses, so the employee authenticates exactly like the agent it dispatches to.
//   - else the project's default_cli_type, with no provider env — the global
//     one-shot preset (applied inside agentproxy) remains the credential source.
//
// Both degrade to ("", nil), i.e. Claude + host/global credentials, which is the
// pre-existing behavior — so an install that never configured providers is
// unaffected.
func (a *employeeAnalyzer) resolveAgentContext(ctx context.Context, scope EmployeeScope) (cliType string, env []string) {
	if a.q == nil {
		return "", nil
	}
	if scope.WorkspaceID != 0 {
		if t, err := a.q.GetWorkspaceCliType(ctx, scope.WorkspaceID); err == nil {
			cliType = strings.TrimSpace(t)
		}
		if rows, err := sceneenv.Resolve(ctx, a.q, scope.WorkspaceID); err == nil {
			for _, r := range rows {
				// NIUNIU_* are niuniu's own control knobs (permission mode, autohost
				// budget, ...), meaningless to a bare generation subprocess.
				if strings.HasPrefix(r.Key, "NIUNIU_") {
					continue
				}
				env = append(env, r.Key+"="+r.Value)
			}
		}
	}
	if cliType == "" && scope.ProjectID != 0 {
		if p, err := a.q.GetProject(ctx, scope.ProjectID); err == nil {
			cliType = strings.TrimSpace(p.DefaultCliType)
		}
	}
	return cliType, env
}

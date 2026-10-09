// Command niuniu-agent is niuniu's self-built coding agent.
//
// Two modes: headless one-shot (`-p`) and the ACP server (`acp`, stdio
// JSON-RPC — niuniu/IDE integration). See
// docs/specs/2026-09-15-niuniu-agent-feasibility-and-plan.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/acp"
	"github.com/niuniu-dev/niuniu/agent/internal/logging"
	"github.com/niuniu-dev/niuniu/agent/internal/loop"
	"github.com/niuniu-dev/niuniu/agent/internal/lsp"
	"github.com/niuniu-dev/niuniu/agent/internal/mcp"
	"github.com/niuniu-dev/niuniu/agent/internal/memory"
	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/perm"
	"github.com/niuniu-dev/niuniu/agent/internal/prompt"
	"github.com/niuniu-dev/niuniu/agent/internal/skills"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// newRegistry advertises the built-in tool suite.
func newRegistry() *tools.Registry {
	return tools.NewRegistry(
		tools.LS{}, tools.Read{}, tools.Grep{}, tools.Glob{},
		tools.Write{}, tools.Edit{}, tools.Bash{}, tools.BashOutput{}, tools.Monitor{}, tools.TodoWrite{},
		tools.NewWebFetch(false), // SSRF protection ON in production wiring
		// Search defaults ON via the zero-key DuckDuckGo backend so reasoning
		// always has fresh material; NIUNIU_AGENT_SEARCH=off opts out.
		tools.NewWebSearch(firstNonEmptyStr(os.Getenv("NIUNIU_AGENT_SEARCH"), "duckduckgo")),
	)
}

// tierModels resolves Claude-CLI-style model tiers (high/fast) lazily from
// the tier env vars over the ambient provider contract (env credentials +
// NIUNIU_AGENT_PROVIDER). A tier without an override — or one that fails to
// build — falls back to the base model: tiers are an optimization, never a
// hard dependency.
type tierModels struct {
	base  model.Model
	mu    sync.Mutex
	built map[string]model.Model // nil value = "resolved to base"
}

func (t *tierModels) resolve(tier string) model.Model {
	if tier == "" {
		return t.base
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if m, ok := t.built[tier]; ok {
		if m == nil {
			return t.base
		}
		return m
	}
	var m model.Model
	if name := model.TierModel(tier); name != "" {
		if built, err := buildModel("", name); err != nil {
			slog.Warn("tier model build failed — falling back to base model", "tier", tier, "model", name, "err", err)
		} else {
			m = built
			slog.Info("tier model ready", "tier", tier, "model", name)
		}
	}
	if t.built == nil {
		t.built = map[string]model.Model{}
	}
	t.built[tier] = m
	if m == nil {
		return t.base
	}
	return m
}

// subagentPreamble turns the base system prompt into a child's.
const subagentPreamble = "\n\nYou are running as a subagent dispatched by a parent agent. You cannot ask the parent questions: complete the given task autonomously with the tools available and end with a final report the parent can act on."

// sessionRegistry assembles one session's toolset: built-in tools plus the
// MCP servers projected into cwd (.mcp.json), the Skill tool over the
// discovered skills, the native memory tools, and the Agent/AgentResult
// tools (subagents). The closer releases the MCP servers. MCP failures
// degrade to built-in tools only — never fatal.
//
// Subagent contract (sharing AND isolation): children share the process
// (same cwd, same model, same permission policy) and inherit the full
// system prompt plus an auto preamble from parentContextOf (recent
// findings); they get HALF the memory recall, never mutate the parent's
// todo list (TodoWrite excluded — the wiring-level exclusion list), cannot
// delegate further (MaxDepth 1), and only their final report ever reaches
// the parent context.
func sessionRegistry(cwd string, m model.Model, perms perm.Checker, parentContextOf func() string) (*tools.Registry, io.Closer) {
	reg := newRegistry()
	// Archived-history retrieval: compact 归档的精确历史可被检索回注。
	// Dir must stay identical to loop.Options.HistoryDir (tools.HistoryDir)
	// — the archive lives in the private state dir, not the project dir.
	reg.Register(tools.HistorySearch{Dir: tools.HistoryDir(cwd)})
	// LSP navigation: .niuniu-agent/lsp.json 声明的语言服务器按需拉起。
	closers := []io.Closer{}
	if cfgs := lsp.LoadConfig(cwd); len(cfgs) > 0 {
		lspMgr := lsp.NewManager(cfgs, cwd)
		reg.Register(tools.LSP{Mgr: lspMgr})
		closers = append(closers, closerFunc(lspMgr.Shutdown))
	}
	mgr := mcp.Start(cwd)
	mgr.RegisterInto(reg)
	skillList := skills.Scan(cwd)
	reg.Register(skills.NewTool(skillList))
	memStore := memory.NewStore(cwd)
	reg.Register(memory.NewSaveTool(memStore))
	reg.Register(memory.NewSearchTool(memStore))
	reg.Register(memory.NewConsolidateTool(memStore))

	tiers := &tierModels{base: m}
	factory := &loop.AgentFactory{
		Model: m,
		Types: loop.BuiltinAgentTypes(),
		// Tier routing: builtin types carry ModelTier hints (explore=fast,
		// plan=high); custom .niuniu-agent/agents types carry `model:` —
		// both resolve here against the tier env vars.
		ModelFor: tiers.resolve,
		// Declarative custom types from the workspace.
		// factory.Types = append(factory.Types, loop.LoadAgentTypes(...) — below.

		// System inheritance: children get the same full session prompt
		// (project context / host capabilities / skills) with HALF the
		// memory recall, plus the subagent role note.
		System:        prompt.BuildSessionCapped(cwd, 2, 1024) + subagentPreamble,
		Perms:         perms,
		MaxDepth:      1,
		ExcludedTools: []string{"TodoWrite"}, // state isolation
	}
	factory.Types = append(factory.Types, loop.LoadAgentTypes(filepath.Join(cwd, ".niuniu-agent", "agents"))...)
	if parentContextOf != nil {
		factory.ContextPreamble = parentContextOf()
	}
	newChild := func() *tools.Registry {
		child := newRegistry()
		mgr.RegisterInto(child)
		child.Register(skills.NewTool(skillList))
		child.Register(memory.NewSaveTool(memStore))
		child.Register(memory.NewSearchTool(memStore))
		for _, name := range factory.ExcludedTools {
			child.Remove(name)
		}
		return child
	}
	factory.NewChildRegistry = newChild
	agentTool := loop.NewAgentTool(factory, 0)
	reg.Register(agentTool)
	reg.Register(loop.NewAgentResultTool(factory))
	closers = append(closers, mgr)
	if len(closers) == 1 {
		return reg, closers[0]
	}
	return reg, multiCloser(closers)
}

// closerFunc adapts a func to io.Closer.
type closerFunc func()

func (f closerFunc) Close() error { f(); return nil }

type multiCloser []io.Closer

func (m multiCloser) Close() error {
	for _, c := range m {
		_ = c.Close()
	}
	return nil
}

func main() {
	// File logging first: the ACP server (and headless runs) may be detached
	// from any terminal, so slog diagnostics must land on disk to be
	// inspectable after a hang or crash.
	logging.InitFileLog("") // ~/.niuniu-agent/logs/agent.log — global, not per-project
	slog.Info("niuniu-agent starting", "args", os.Args)

	if len(os.Args) > 1 && os.Args[1] == "acp" {
		runACP()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "memory-consolidate" {
		runMemoryConsolidate()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "eval" {
		runEval()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "explore" {
		runExplore()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "evolve" {
		runEvolve()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "profiles" {
		fs := flag.NewFlagSet("profiles", flag.ContinueOnError)
		configPath := fs.String("config", "", "profile config path override")
		provider := fs.String("provider", "", "model provider override")
		modelName := fs.String("model", "", "model name override")
		profile := fs.String("profile", "", "profile to resolve as active")
		if err := fs.Parse(os.Args[2:]); err != nil {
			fail(err)
		}
		cwd, _ := os.Getwd()
		cfg, err := model.ResolveFromCwd(cwd, *configPath, model.Flags{
			Profile:  firstNonEmptyStr(*profile, os.Getenv("NIUNIU_AGENT_PROFILE")),
			Provider: *provider, Model: *modelName,
		})
		if err != nil {
			// 列表仍可渲染——active 未知时省略 active 行。
			cfg = model.Config{}
		}
		fmt.Print(model.RenderProfiles(cwd, *configPath, cfg))
		return
	}

	var (
		promptText  = flag.String("p", "", "one-shot prompt: run headless and print the final answer")
		provider    = flag.String("provider", "", "model provider: anthropic (default) or openai")
		modelName   = flag.String("model", "", "model name override")
		maxTurns    = flag.Int("max-turns", 0, "max model round-trips (0 = unlimited, the default; NIUNIU_AGENT_MAX_TURNS env also sets a cap)")
		timeout     = flag.Duration("timeout", 5*time.Minute, "overall timeout for the run")
		yes         = flag.Bool("y", false, "auto-approve mutating tools (Write/Edit/Bash); without it headless mode refuses them")
		reflectOn   = flag.Bool("reflect", false, "after the run, distill a durable lesson into memory (one extra model call)")
		printSystem = flag.Bool("print-system", false, "print the assembled system prompt to stderr (debug)")
		resume      = flag.String("resume", "", "resume a saved session (id under <cwd>/.niuniu-agent/sessions, or \"latest\")")
		configPath  = flag.String("config", "", "profile config path (default: <cwd>/.niuniu-agent/config.json then ~/.niuniu-agent/config.json)")
		profile     = flag.String("profile", "", "model profile from config.json")
	)
	flag.Parse()

	if *promptText == "-" {
		// Read the prompt from stdin (niuniu's one-shot observation path pipes
		// it in rather than passing it as an argv value).
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fail(fmt.Errorf("read stdin prompt: %w", err))
		}
		*promptText = strings.TrimSpace(string(data))
	}

	if *promptText == "" {
		fmt.Fprintf(os.Stderr, `niuniu-agent — niuniu's self-built coding agent (P1)

Usage:
  niuniu-agent -p "prompt" [-provider anthropic|openai] [-model name] [-max-turns n] [-timeout 5m] [-y]
  niuniu-agent acp          ACP server over stdio (niuniu / IDE integration)

Configuration (env):
  NIUNIU_AGENT_PROVIDER  anthropic (default) | openai
  NIUNIU_AGENT_MAX_TURNS optional cap on model round-trips per prompt (unset = unlimited)
  ANTHROPIC_BASE_URL / ANTHROPIC_API_KEY | ANTHROPIC_AUTH_TOKEN / ANTHROPIC_MODEL
  OPENAI_BASE_URL / OPENAI_API_KEY / OPENAI_MODEL
`)
		os.Exit(2)
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	cwd, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	// Model config: profile chain (flag > env > project > global) resolved
	// before anything that needs the model.
	flags := model.Flags{Profile: *profile, Provider: *provider, Model: *modelName}
	cfg, err := model.ResolveFromCwd(cwd, *configPath, flags)
	if err != nil {
		fail(err)
	}
	m, err := model.NewForProviderOrLegacy(cfg)
	if err != nil {
		fail(err)
	}
	// Session toolset: built-ins + MCP servers + Skill + Agent tools; server
	// lifetime is the process's. Subagents inherit the parent's policy so
	// they cannot bypass the -y write choice.
	var sess *loop.Session
	reg, closer := sessionRegistry(cwd, m, perm.NewPolicy(*yes), func() string {
		if sess == nil {
			return ""
		}
		// Auto context inheritance: recent parent findings ride into every
		// subagent (the factory caps the preamble).
		return sess.Transcript()
	})
	defer closer.Close()

	system := prompt.BuildSession(cwd)

	sess = loop.NewSession(m, reg, system)
	// Session resume: rebuild the prior conversation so follow-up runs keep
	// full context.
	sessionsDir := tools.SessionsDir(cwd) // ~/.niuniu-agent/projects/<escaped-cwd>/sessions
	if *resume != "" {
		id := *resume
		if id == "latest" {
			latest, err := loop.LatestSessionID(sessionsDir)
			if err != nil {
				fail(fmt.Errorf("resume latest: %w", err))
			}
			id = latest
		}
		st, err := loop.LoadSession(sessionsDir, id)
		if err != nil {
			fail(fmt.Errorf("resume %s: %w", id, err))
		}
		sess = loop.RestoreSession(m, reg, st)
		fmt.Fprintf(os.Stderr, "[session] resumed %s (%d prior messages)\n", st.ID, len(st.Messages))
	}

	if *printSystem {
		// Observability for tests/debugging: what the model actually sees.
		fmt.Fprintln(os.Stderr, "--- system ---\n"+system+"\n--- end system ---")
	}

	// Streaming: SSE by default (NIUNIU_AGENT_STREAM=0 to disable); the
	// first-token latency is printed for observability.
	streamOn := cfg.Stream
	var firstToken time.Time
	// Session form (not Run) so the transcript is available for the
	// optional reflection pass.
	res, err := sess.Prompt(ctx, *promptText,
		loop.Options{
			MaxTurns:         *maxTurns,
			Perms:            perm.NewPolicy(*yes),
			Stream:           streamOn,
			CompactStatePath: tools.CompactStatePath(cwd),
			ImpressionPath:   tools.ImpressionPath(cwd),
			ContextEditing:   os.Getenv("NIUNIU_AGENT_CONTEXT_EDITING") == "1",
			HistoryDir:       tools.HistoryDir(cwd),
			OnEvent: func(e loop.Event) {
				if streamOn && e.Delta && firstToken.IsZero() && (e.Kind == loop.EventText || e.Kind == loop.EventThinking) {
					firstToken = time.Now()
					fmt.Fprintf(os.Stderr, "[stream] first token in %s\n", time.Since(start).Round(time.Millisecond))
				}
				if e.Kind == loop.EventThinking {
					// Reasoning observability: one truncated line per thinking
					// block (stdout stays the answer; telemetry → stderr).
					line := e.Text
					if len(line) > 300 {
						line = line[:300] + "…"
					}
					fmt.Fprintf(os.Stderr, "[thinking] %s\n", strings.ReplaceAll(line, "\n", " "))
				}
			},
		})
	if err != nil {
		fail(err)
	}
	fmt.Println(res.Text)
	// Usage summary goes to stderr: stdout is the answer itself (often piped
	// into other tools); telemetry must not corrupt it.
	u := res.Usage
	cacheRatio := 0.0
	if in := u.InputTokens + u.CacheReadTokens; in > 0 {
		cacheRatio = float64(u.CacheReadTokens) / float64(in)
	}
	fmt.Fprintf(os.Stderr, "[usage] rounds=%d input=%d output=%d cache-read=%d cache-write=%d cache-hit=%.0f%%\n",
		res.Rounds, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheCreationTokens, cacheRatio*100)

	// Persist the session (with this turn's exchanges) for -resume.
	sessID := fmt.Sprintf("s-%s", time.Now().UTC().Format("20060102-150405"))
	if err := loop.SaveSession(sessionsDir, sess.ExportState(sessID)); err != nil {
		fmt.Fprintf(os.Stderr, "[session] save failed: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "[session] saved %s — continue with -resume %s\n", sessID, sessID)
	}

	// Optional reflection pass: one extra model call distills a durable
	// lesson from the transcript into memory (same title updates in place).
	if *reflectOn {
		id, rerr := memory.Reflect(ctx, m, sess.Transcript(), memory.NewStore(cwd))
		switch {
		case rerr != nil:
			fmt.Fprintf(os.Stderr, "[reflect] failed: %v\n", rerr)
		case id == "":
			fmt.Fprintf(os.Stderr, "[reflect] nothing worth remembering\n")
		default:
			fmt.Fprintf(os.Stderr, "[reflect] saved memory %s\n", id)
		}
	}
}

// runMemoryConsolidate is the CLI housekeeping command: merge same-topic
// memories, expire stale ones, and enforce the capacity cap.
func runMemoryConsolidate() {
	fs := flag.NewFlagSet("memory-consolidate", flag.ContinueOnError)
	maxAge := fs.Int("max-age-days", 0, "expire entries untouched for N days")
	noMerge := fs.Bool("no-merge", false, "skip the same-topic merge pass")
	if err := fs.Parse(os.Args[2:]); err != nil {
		fail(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	res, err := memory.NewStore(cwd).Consolidate(memory.ConsolidateOptions{
		MaxAgeDays:   *maxAge,
		MaxEntries:   memory.DefaultMaxEntriesPerLayer,
		MergeSimilar: !*noMerge,
	})
	if err != nil {
		fail(err)
	}
	fmt.Printf("memory consolidated: before=%d after=%d merged=%d expired=%d evicted=%d\n",
		res.Before, res.After, res.Merged, res.Expired, res.Evicted)
}

// runACP serves the ACP protocol over stdin/stdout.
func runACP() {
	fs := flag.NewFlagSet("acp", flag.ContinueOnError)
	provider := fs.String("provider", "", "model provider: anthropic (default) or openai")
	modelName := fs.String("model", "", "model name override")
	configPath := fs.String("config", "", "profile config path override")
	if err := fs.Parse(os.Args[2:]); err != nil {
		fail(err)
	}
	cwd, _ := os.Getwd()

	srv := acp.New(os.Stdin, os.Stdout, newRegistry(), prompt.BuildSession,
		func() (model.Model, error) {
			return buildModel(*provider, *modelName)
		},
		// Per-session tool projection: MCP servers + Skill + Agent tools for
		// the session cwd. The returned closer ties server lifetime to the
		// session's (== the process's in niuniu's one-agent-per-workspace
		// deployment). Subagents run with the conservative policy: ACP
		// approvals are dynamic (per prompt), so a subagent's mutating calls
		// are refused rather than silently allowed — P4 wires the approval
		// flow through to child sessions.
		func(cwd string, m model.Model) (*tools.Registry, io.Closer, error) {
			reg, closer := sessionRegistry(cwd, m, perm.NewPolicy(false), nil)
			return reg, closer, nil
		},
		// set_model profile resolver: model names map through config.json
		// profiles; bare provider names also work (anthropic/openai).
		func(modelName string) (model.Model, error) {
			cfg, err := model.ResolveFromCwd(cwd, *configPath, model.Flags{Profile: modelName})
			if err != nil {
				return nil, err
			}
			return model.NewForProviderOrLegacy(cfg)
		})
	if err := srv.Serve(context.Background()); err != nil {
		fail(err)
	}
}

func mustModel(provider, modelName string) model.Model {
	m, err := buildModel(provider, modelName)
	if err != nil {
		fail(err)
	}
	return m
}

func buildModel(provider, modelName string) (model.Model, error) {
	cfg, err := model.LoadConfig(provider, modelName)
	if err != nil {
		return nil, err
	}
	if cfg.Provider == model.ProviderOpenAI {
		return model.NewOpenAI(cfg), nil
	}
	return model.NewAnthropic(cfg), nil
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "niuniu-agent: %v\n", err)
	os.Exit(1)
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

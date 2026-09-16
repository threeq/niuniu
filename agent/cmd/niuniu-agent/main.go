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
	"os"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/acp"
	"github.com/niuniu-dev/niuniu/agent/internal/loop"
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
		tools.Write{}, tools.Edit{}, tools.Bash{}, tools.TodoWrite{},
	)
}

// subagentPreamble turns the base system prompt into a child's.
const subagentPreamble = "\n\nYou are running as a subagent dispatched by a parent agent. You cannot ask the parent questions: complete the given task autonomously with the tools available and end with a final report the parent can act on."

// sessionRegistry assembles one session's toolset: built-in tools plus the
// MCP servers projected into cwd (.mcp.json), the Skill tool over the
// discovered skills, the native memory tools, and the Agent tool (subagent).
// The closer releases the MCP servers. MCP failures degrade to built-in
// tools only — never fatal. Child sessions (via Agent) get the same
// capabilities minus the Agent tool, which is what enforces the recursion
// depth limit of 1.
func sessionRegistry(cwd string, m model.Model, perms perm.Checker) (*tools.Registry, io.Closer) {
	reg := newRegistry()
	mgr := mcp.Start(cwd)
	mgr.RegisterInto(reg)
	skillList := skills.Scan(cwd)
	reg.Register(skills.NewTool(skillList))
	memStore := memory.NewStore(cwd)
	reg.Register(memory.NewSaveTool(memStore))
	reg.Register(memory.NewSearchTool(memStore))

	system := prompt.BuildSession(cwd) + subagentPreamble
	newChild := func() *tools.Registry {
		child := newRegistry()
		mgr.RegisterInto(child)
		child.Register(skills.NewTool(skillList))
		child.Register(memory.NewSaveTool(memStore))
		child.Register(memory.NewSearchTool(memStore))
		return child
	}
	reg.Register(loop.NewAgentTool(&loop.AgentFactory{
		Model:            m,
		System:           system,
		NewChildRegistry: newChild,
		Perms:            perms,
		MaxDepth:         1,
	}, 0))
	return reg, mgr
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "acp" {
		runACP()
		return
	}

	var (
		promptText  = flag.String("p", "", "one-shot prompt: run headless and print the final answer")
		provider    = flag.String("provider", "", "model provider: anthropic (default) or openai")
		modelName   = flag.String("model", "", "model name override")
		maxTurns    = flag.Int("max-turns", 0, "max model round-trips (default 16)")
		timeout     = flag.Duration("timeout", 5*time.Minute, "overall timeout for the run")
		yes         = flag.Bool("y", false, "auto-approve mutating tools (Write/Edit/Bash); without it headless mode refuses them")
		reflectOn   = flag.Bool("reflect", false, "after the run, distill a durable lesson into memory (one extra model call)")
		printSystem = flag.Bool("print-system", false, "print the assembled system prompt to stderr (debug)")
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
  ANTHROPIC_BASE_URL / ANTHROPIC_API_KEY | ANTHROPIC_AUTH_TOKEN / ANTHROPIC_MODEL
  OPENAI_BASE_URL / OPENAI_API_KEY / OPENAI_MODEL
`)
		os.Exit(2)
	}

	m := mustModel(*provider, *modelName)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	cwd, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	// Session toolset: built-ins + MCP servers + Skill + Agent tools; server
	// lifetime is the process's. Subagents inherit the parent's policy so
	// they cannot bypass the -y write choice.
	reg, closer := sessionRegistry(cwd, m, perm.NewPolicy(*yes))
	defer closer.Close()

	system := prompt.BuildSession(cwd)
	if *printSystem {
		// Observability for tests/debugging: what the model actually sees.
		fmt.Fprintln(os.Stderr, "--- system ---\n"+system+"\n--- end system ---")
	}

	// Session form (not Run) so the transcript is available for the
	// optional reflection pass.
	sess := loop.NewSession(m, reg, system)
	res, err := sess.Prompt(ctx, *promptText,
		loop.Options{MaxTurns: *maxTurns, Perms: perm.NewPolicy(*yes)})
	if err != nil {
		fail(err)
	}
	fmt.Println(res.Text)
	// Usage summary goes to stderr: stdout is the answer itself (often piped
	// into other tools); telemetry must not corrupt it.
	u := res.Usage
	fmt.Fprintf(os.Stderr, "[usage] rounds=%d input=%d output=%d cache-read=%d cache-write=%d\n",
		res.Rounds, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheCreationTokens)

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

// runACP serves the ACP protocol over stdin/stdout.
func runACP() {
	fs := flag.NewFlagSet("acp", flag.ContinueOnError)
	provider := fs.String("provider", "", "model provider: anthropic (default) or openai")
	modelName := fs.String("model", "", "model name override")
	if err := fs.Parse(os.Args[2:]); err != nil {
		fail(err)
	}
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
			reg, closer := sessionRegistry(cwd, m, perm.NewPolicy(false))
			return reg, closer, nil
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

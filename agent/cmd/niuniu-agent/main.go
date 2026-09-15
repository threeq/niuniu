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

// sessionRegistry assembles one session's toolset: built-in tools plus the
// MCP servers projected into cwd (.mcp.json) plus the Skill tool over the
// discovered skills. The closer releases the MCP servers. MCP failures
// degrade to built-in tools only — never fatal.
func sessionRegistry(cwd string) (*tools.Registry, io.Closer) {
	reg := newRegistry()
	mgr := mcp.Start(cwd)
	mgr.RegisterInto(reg)
	reg.Register(skills.NewTool(skills.Scan(cwd)))
	return reg, mgr
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "acp" {
		runACP()
		return
	}

	var (
		promptText = flag.String("p", "", "one-shot prompt: run headless and print the final answer")
		provider   = flag.String("provider", "", "model provider: anthropic (default) or openai")
		modelName  = flag.String("model", "", "model name override")
		maxTurns   = flag.Int("max-turns", 0, "max model round-trips (default 16)")
		timeout    = flag.Duration("timeout", 5*time.Minute, "overall timeout for the run")
		yes        = flag.Bool("y", false, "auto-approve mutating tools (Write/Edit/Bash); without it headless mode refuses them")
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
	// Session toolset: built-ins + MCP servers + Skill tool; server lifetime
	// is the process's.
	reg, closer := sessionRegistry(cwd)
	defer closer.Close()

	res, err := loop.Run(ctx, m, reg, prompt.Build(cwd), *promptText,
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
}

// runACP serves the ACP protocol over stdin/stdout.
func runACP() {
	fs := flag.NewFlagSet("acp", flag.ContinueOnError)
	provider := fs.String("provider", "", "model provider: anthropic (default) or openai")
	modelName := fs.String("model", "", "model name override")
	if err := fs.Parse(os.Args[2:]); err != nil {
		fail(err)
	}
	srv := acp.New(os.Stdin, os.Stdout, newRegistry(), prompt.Build,
		func() (model.Model, error) {
			return buildModel(*provider, *modelName)
		},
		// Per-session tool projection: MCP servers + Skill tool for the
		// session cwd. The returned closer ties server lifetime to the
		// session's (== the process's in niuniu's one-agent-per-workspace
		// deployment).
		func(cwd string, m model.Model) (*tools.Registry, io.Closer, error) {
			reg, closer := sessionRegistry(cwd)
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

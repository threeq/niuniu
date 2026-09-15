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
	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/perm"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// systemPrompt is niuniu-agent's own system prompt, written for the models
// niuniu actually drives (GLM and friends) — clean-room, not copied from any
// closed-source agent.
const systemPrompt = `You are niuniu-agent, a careful coding agent working on the user's machine.

Rules:
- Reply in the language the user writes in.
- When a question depends on local files, use the provided tools to inspect them; never invent file listings, file contents, or command output.
- For multi-step work, maintain the task list with TodoWrite: mark items in_progress before starting and completed right after finishing.
- Prefer targeted edits (Edit) over rewriting whole files (Write).
- Keep answers short and factual.`

// newRegistry advertises the full P1 tool suite.
func newRegistry() *tools.Registry {
	return tools.NewRegistry(
		tools.LS{}, tools.Read{}, tools.Grep{}, tools.Glob{},
		tools.Write{}, tools.Edit{}, tools.Bash{}, tools.TodoWrite{},
	)
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "acp" {
		runACP()
		return
	}

	var (
		prompt    = flag.String("p", "", "one-shot prompt: run headless and print the final answer")
		provider  = flag.String("provider", "", "model provider: anthropic (default) or openai")
		modelName = flag.String("model", "", "model name override")
		maxTurns  = flag.Int("max-turns", 0, "max model round-trips (default 16)")
		timeout   = flag.Duration("timeout", 5*time.Minute, "overall timeout for the run")
		yes       = flag.Bool("y", false, "auto-approve mutating tools (Write/Edit/Bash); without it headless mode refuses them")
	)
	flag.Parse()

	if *prompt == "-" {
		// Read the prompt from stdin (niuniu's one-shot observation path pipes
		// it in rather than passing it as an argv value).
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fail(fmt.Errorf("read stdin prompt: %w", err))
		}
		*prompt = strings.TrimSpace(string(data))
	}

	if *prompt == "" {
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
	reg := newRegistry()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	answer, err := loop.Run(ctx, m, reg, systemPrompt, *prompt,
		loop.Options{MaxTurns: *maxTurns, Perms: perm.NewPolicy(*yes)})
	if err != nil {
		fail(err)
	}
	fmt.Println(answer)
}

// runACP serves the ACP protocol over stdin/stdout.
func runACP() {
	fs := flag.NewFlagSet("acp", flag.ContinueOnError)
	provider := fs.String("provider", "", "model provider: anthropic (default) or openai")
	modelName := fs.String("model", "", "model name override")
	if err := fs.Parse(os.Args[2:]); err != nil {
		fail(err)
	}
	srv := acp.New(os.Stdin, os.Stdout, newRegistry(), systemPrompt, func() (model.Model, error) {
		return buildModel(*provider, *modelName)
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

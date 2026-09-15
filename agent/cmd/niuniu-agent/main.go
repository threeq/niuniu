// Command niuniu-agent is niuniu's self-built coding agent.
//
// P0 ships the headless single-shot mode (`-p`). The ACP server (`acp`,
// niuniu/IDE integration) and the interactive TUI arrive in P1 — see
// docs/specs/2026-09-15-niuniu-agent-feasibility-and-plan.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/loop"
	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// systemPrompt is niuniu-agent's own system prompt, written for the models
// niuniu actually drives (GLM and friends) — clean-room, not copied from any
// closed-source agent.
const systemPrompt = `You are niuniu-agent, a careful coding agent working on the user's machine.

Rules:
- Reply in the language the user writes in.
- When a question depends on local files, use the provided tools to inspect them; never invent file listings or file contents.
- Keep answers short and factual.`

func main() {
	if len(os.Args) > 1 && os.Args[1] == "acp" {
		fmt.Fprintln(os.Stderr, "niuniu-agent acp: not implemented yet — planned for P1 "+
			"(docs/specs/2026-09-15-niuniu-agent-feasibility-and-plan.md §6)")
		os.Exit(2)
	}

	var (
		prompt    = flag.String("p", "", "one-shot prompt: run headless and print the final answer")
		provider  = flag.String("provider", "", "model provider: anthropic (default) or openai")
		modelName = flag.String("model", "", "model name override")
		maxTurns  = flag.Int("max-turns", 0, "max model round-trips (default 16)")
		timeout   = flag.Duration("timeout", 3*time.Minute, "overall timeout for the run")
	)
	flag.Parse()

	if *prompt == "" {
		fmt.Fprintf(os.Stderr, `niuniu-agent — niuniu's self-built coding agent (P0)

Usage:
  niuniu-agent -p "prompt" [-provider anthropic|openai] [-model name] [-max-turns n] [-timeout 3m]
  niuniu-agent acp          (P1: ACP server over stdio)

Configuration (env):
  NIUNIU_AGENT_PROVIDER  anthropic (default) | openai
  ANTHROPIC_BASE_URL / ANTHROPIC_API_KEY | ANTHROPIC_AUTH_TOKEN / ANTHROPIC_MODEL
  OPENAI_BASE_URL / OPENAI_API_KEY / OPENAI_MODEL
`)
		os.Exit(2)
	}

	cfg, err := model.LoadConfig(*provider, *modelName)
	if err != nil {
		fail(err)
	}
	var m model.Model
	switch cfg.Provider {
	case model.ProviderOpenAI:
		m = model.NewOpenAI(cfg)
	default:
		m = model.NewAnthropic(cfg)
	}

	reg := tools.NewRegistry(tools.LS{}, tools.Read{})

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	answer, err := loop.Run(ctx, m, reg, systemPrompt, *prompt, loop.Options{MaxTurns: *maxTurns})
	if err != nil {
		fail(err)
	}
	fmt.Println(answer)
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "niuniu-agent: %v\n", err)
	os.Exit(1)
}

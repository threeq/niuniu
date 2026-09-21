package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/eval"
	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// runEvolve implements `niuniu-agent evolve`: one full self-evolution round
// (OpenRSI-inspired) — propose a PROMPT.md rewrite (think-first), run the
// PUBLIC task set with it, then gate adoption on the PRIVATE held-out set.
func runEvolve() {
	fs := flag.NewFlagSet("evolve", flag.ContinueOnError)
	publicDir := fs.String("public", "eval/tasks", "public task dir (iteration)")
	privateDir := fs.String("private", "eval/hidden", "private held-out task dir (adoption gate)")
	timeout := fs.Duration("timeout", 3*time.Minute, "per-task timeout")
	provider := fs.String("provider", "", "model provider: anthropic (default) or openai")
	modelName := fs.String("model", "", "model name override")
	if err := fs.Parse(os.Args[2:]); err != nil {
		fail(err)
	}
	cwd, _ := os.Getwd()
	m := mustModel(*provider, *modelName)
	reg := tools.NewRegistry(
		tools.LS{}, tools.Read{}, tools.Grep{}, tools.Glob{},
		tools.Write{}, tools.Edit{}, tools.Bash{}, tools.TodoWrite{},
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 现役基线：旧 system（无 PROMPT.md）跑私有集。
	baseSystem := "You are niuniu-agent in an evaluation sandbox. Complete the task using the tools."
	privateTasks, err := eval.LoadTasks(*privateDir)
	if err != nil {
		fail(fmt.Errorf("load private tasks: %w", err))
	}
	oldPrivate := eval.RunSetWithSystem(ctx, m, reg, privateTasks, *timeout, baseSystem)
	fmt.Fprintf(os.Stderr, "[evolve] incumbent private pass rate: %d/%d\n", oldPrivate.Passed, oldPrivate.Total)

	// 提案：模型读公开集任务描述，产出 think-first 改写提案。
	publicTasks, err := eval.LoadTasks(*publicDir)
	if err != nil {
		fail(fmt.Errorf("load public tasks: %w", err))
	}
	var taskBrief strings.Builder
	for _, t := range publicTasks {
		fmt.Fprintf(&taskBrief, "- %s: %s\n", t.Name, t.Prompt)
	}
	proposalRaw, err := callModel(ctx, m, evolveProposalSystem, taskBrief.String())
	if err != nil {
		fail(err)
	}
	mechanism, guidance, err := parseEvolveProposal(proposalRaw)
	if err != nil {
		fail(fmt.Errorf("proposal rejected: %w", err))
	}
	fmt.Fprintf(os.Stderr, "[evolve] proposal accepted for evaluation: %s\n", mechanism)

	// 公开集迭代：新 system（含 PROMPT.md 内容）跑公开任务。
	newSystem := baseSystem + "\n# Evolved task guidance (candidate)\n\n" + guidance + "\n"
	newPublic := eval.RunSetWithSystem(ctx, m, reg, publicTasks, *timeout, newSystem)
	fmt.Fprintf(os.Stderr, "[evolve] public pass rate with candidate: %d/%d\n", newPublic.Passed, newPublic.Total)

	// 私有集判定：新 system 通过率 >= 现役 → 采纳（写 PROMPT.md）。
	newPrivate := eval.RunSetWithSystem(ctx, m, reg, privateTasks, *timeout, newSystem)
	oldRate := rate(oldPrivate.Passed, oldPrivate.Total)
	newRate := rate(newPrivate.Passed, newPrivate.Total)
	promptPath := filepath.Join(cwd, ".niuniu-agent", "PROMPT.md")
	if newRate >= oldRate {
		if err := os.MkdirAll(filepath.Dir(promptPath), 0o755); err != nil {
			fail(err)
		}
		if err := os.WriteFile(promptPath, []byte(guidance), 0o644); err != nil {
			fail(err)
		}
		fmt.Printf("ADOPTED: private pass rate %.2f%% >= incumbent %.2f%%\n  PROMPT.md written: %s\n",
			newRate, oldRate, promptPath)
	} else {
		fmt.Printf("REJECTED: private pass rate %.2f%% < incumbent %.2f%% — incumbent PROMPT.md kept\n",
			newRate, oldRate)
	}
}

func rate(passed, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(passed) / float64(total) * 100
}

// callModel 是一次无工具的单轮模型调用（提案生成等）。
func callModel(ctx context.Context, m model.Model, system, userText string) (string, error) {
	resp, err := m.Complete(ctx, model.Request{
		System:    system,
		Messages:  []model.Message{{Role: model.RoleUser, Blocks: []model.Block{{Type: model.BlockText, Text: userText}}}},
		MaxTokens: 4096,
	})
	if err != nil {
		return "", err
	}
	return resp.Message.Text(), nil
}

// evolveProposalSystem 驱动 PROMPT.md 改写提案（think-first 协议）。
const evolveProposalSystem = `You are evolving niuniu-agent's system prompt.

Below are the public eval tasks (name + instruction). Propose ONE rewrite of
the task-guidance system prompt that would measurably improve pass rate on
tasks like these.

Output EXACTLY:
MECHANISM: <causal mechanism — what guidance changes and why it helps>
EXPECTED GAIN: <quantified expected improvement on this task family>
FALSIFICATION: <what observed result would prove this proposal wrong>
GUIDANCE:
<the full replacement task-guidance text, 10-40 lines>`

// parseEvolveProposal 拆出 mechanism 与 guidance。
func parseEvolveProposal(raw string) (mechanism, guidance string, err error) {
	if !strings.Contains(raw, "MECHANISM:") || !strings.Contains(raw, "GUIDANCE:") {
		return "", "", fmt.Errorf("proposal missing MECHANISM/GUIDANCE sections")
	}
	i := strings.Index(raw, "GUIDANCE:")
	guidance = strings.TrimSpace(raw[i+len("GUIDANCE:"):])
	j := strings.Index(raw, "MECHANISM:")
	k := strings.Index(raw, "GUIDANCE:")
	if j < 0 || k < j {
		return "", "", fmt.Errorf("malformed proposal")
	}
	mechanism = strings.TrimSpace(raw[j+len("MECHANISM:") : k])
	return mechanism, guidance, nil
}

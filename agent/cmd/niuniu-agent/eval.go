package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/eval"
	"github.com/niuniu-dev/niuniu/agent/internal/memory"
	"github.com/niuniu-dev/niuniu/agent/internal/rsi"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// runEval implements the `niuniu-agent eval` subcommand: run the task corpus
// in per-task sandboxes, judge with deterministic rules, and emit
// JSON + markdown reports comparable against a baseline.
func runEval() {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	tasksDir := fs.String("tasks", "eval/tasks", "directory of task markdown files")
	outDir := fs.String("out", "eval/reports", "report output directory")
	baseline := fs.String("baseline", "", "baseline summary.json to compare against")
	filter := fs.String("filter", "", "only run tasks whose name contains this substring")
	autoExplore := fs.Bool("auto-explore", false, "after the run: on a repeated failure pattern (overlapping the previous run, outside cooldown), trigger an RSI explore to distill fixes")
	exploreCooldown := fs.Duration("explore-cooldown", 24*time.Hour, "minimum interval between auto-explore runs")
	timeout := fs.Duration("timeout", 3*time.Minute, "per-task timeout")
	provider := fs.String("provider", "", "model provider: anthropic (default) or openai")
	modelName := fs.String("model", "", "model name override")
	if err := fs.Parse(os.Args[2:]); err != nil {
		fail(err)
	}

	tasks, err := eval.LoadTasks(*tasksDir)
	if err != nil {
		fail(err)
	}
	if *filter != "" {
		var kept []eval.Task
		for _, t := range tasks {
			if strings.Contains(t.Name, *filter) {
				kept = append(kept, t)
			}
		}
		tasks = kept
	}
	if len(tasks) == 0 {
		fail(fmt.Errorf("no tasks matched (dir %s, filter %q)", *tasksDir, *filter))
	}

	m := mustModel(*provider, *modelName)
	memoryDir := filepath.Join(func() string { d, _ := os.Getwd(); return d }(), ".niuniu-agent", "memory")
	reg := tools.NewRegistry(
		tools.LS{}, tools.Read{}, tools.Grep{}, tools.Glob{},
		tools.Write{}, tools.Edit{}, tools.Bash{}, tools.TodoWrite{},
	)

	summary := eval.Summary{GeneratedAt: time.Now()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for i, t := range tasks {
		fmt.Fprintf(os.Stderr, "[eval %d/%d] %s\n", i+1, len(tasks), t.Name)
		start := time.Now()
		res := eval.RunTask(ctx, m, reg, t, *timeout, memoryDir)
		summary.Results = append(summary.Results, res)
		summary.Total++
		if res.Pass {
			summary.Passed++
		} else {
			summary.Failed++
		}
		fmt.Fprintf(os.Stderr, "[eval] %s → %v (%s)\n", t.Name, passFail(res.Pass), time.Since(start).Round(time.Millisecond))
	}
	summary.TotalDuration = time.Since(summary.GeneratedAt).Round(time.Millisecond).String()
	for _, r := range summary.Results {
		summary.TotalUsage.Input += int64(r.Usage.InputTokens)
		summary.TotalUsage.Output += int64(r.Usage.OutputTokens)
		summary.TotalUsage.CacheRead += int64(r.Usage.CacheReadTokens)
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fail(err)
	}
	stamp := time.Now().UTC().Format("20060102-150405")
	jsonPath := filepath.Join(*outDir, "summary-"+stamp+".json")
	mdPath := filepath.Join(*outDir, "report-"+stamp+".md")
	if err := summary.WriteJSON(jsonPath); err != nil {
		fail(err)
	}
	if err := summary.WriteMarkdown(mdPath); err != nil {
		fail(err)
	}
	fmt.Printf("eval done: %d/%d passed (%.0f%%)\n  json: %s\n  md:   %s\n",
		summary.Passed, summary.Total, float64(summary.Passed)/float64(summary.Total)*100, jsonPath, mdPath)

	if *baseline != "" {
		if line, err := summary.CompareAgainst(*baseline); err == nil {
			fmt.Println(line)
		} else {
			fmt.Fprintf(os.Stderr, "[eval] baseline compare failed: %v\n", err)
		}
	}

	// RSI auto-trigger (opt-in): repeated failure pattern + cooldown gate.
	if *autoExplore {
		triggerAutoExplore(summary, *outDir, *exploreCooldown, *provider, *modelName)
	}
}

// triggerAutoExplore reads/persists the trigger state next to the reports
// and launches an explore run when the policy fires.
func triggerAutoExplore(summary eval.Summary, outDir string, cooldown time.Duration, provider, modelName string) {
	statePath := filepath.Join(outDir, "rsi-trigger-state.json")
	var lastExplore time.Time
	var lastFailures []string
	if data, err := os.ReadFile(statePath); err == nil {
		var st rsi.TriggerState
		if json.Unmarshal(data, &st) == nil {
			lastExplore = st.LastExplore
			lastFailures = st.LastFailures
		}
	}
	if !rsi.ShouldTrigger(failedNamesOf(summary), lastFailures, lastExplore, cooldown) {
		fmt.Fprintln(os.Stderr, "[eval] auto-explore: trigger conditions not met (all green / one-off / cooldown)")
		return
	}
	fmt.Fprintln(os.Stderr, "[eval] auto-explore: repeated failure pattern — running RSI explore")
	m := mustModel(provider, modelName)
	cwd, _ := os.Getwd()
	reg := newRegistry()
	store := memory.NewStore(cwd)
	runner := &rsi.Runner{
		Model: m,
		Reg:   reg,
		Store: store,
		Cwd:   cwd,
		Opts: rsi.Options{
			Broad:        3,
			Deep:         2,
			MemoryFreeze: true,
		},
	}
	report, err := runner.Run(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "[eval] auto-explore failed: %v\n", err)
		return
	}
	st := rsi.TriggerState{LastExplore: time.Now(), LastFailures: failedNamesOf(summary)}
	data, _ := json.MarshalIndent(st, "", "  ")
	_ = os.WriteFile(statePath, data, 0o644)
	fmt.Fprintf(os.Stderr, "[eval] auto-explore done: pass rate %.0f%%, lessons %d\n",
		report.PassRate(), len(report.Lessons))
}

func failedNamesOf(s eval.Summary) []string {
	var out []string
	for _, r := range s.Results {
		if !r.Pass {
			out = append(out, r.Name)
		}
	}
	return out
}

func passFail(pass bool) string {
	if pass {
		return "PASS"
	}
	return "FAIL"
}

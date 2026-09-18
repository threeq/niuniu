package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/memory"
	"github.com/niuniu-dev/niuniu/agent/internal/rsi"
)

// runExplore implements `niuniu-agent explore`: RSI warm-up — the model
// generates practice tasks (Curriculum), attempts them in eval sandboxes
// (Actor), and rule checks verify them (Verifier). Only verified passes
// distill lessons into project memory.
func runExplore() {
	fs := flag.NewFlagSet("explore", flag.ContinueOnError)
	broad := fs.Int("broad", 5, "number of diverse practice tasks generated first")
	deep := fs.Int("deep", 3, "number of focus tasks targeting failed areas")
	noFreeze := fs.Bool("memory-unfrozen", false, "allow the Actor to write memory directly (NOT recommended)")
	perTask := fs.Duration("per-task-timeout", 2*time.Minute, "timeout per practice task")
	timeout := fs.Duration("timeout", 15*time.Minute, "overall timeout for the run")
	provider := fs.String("provider", "", "model provider: anthropic (default) or openai")
	modelName := fs.String("model", "", "model name override")
	if err := fs.Parse(os.Args[2:]); err != nil {
		fail(err)
	}

	m := mustModel(*provider, *modelName)
	cwd, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	reg := newRegistry()
	store := memory.NewStore(cwd)

	freeze := !*noFreeze
	runner := &rsi.Runner{
		Model: m,
		Reg:   reg,
		Store: store,
		Cwd:   cwd,
		Opts: rsi.Options{
			Broad:          *broad,
			Deep:           *deep,
			MemoryFreeze:   freeze,
			PerTaskTimeout: *perTask,
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	report, err := runner.Run(ctx)
	if err != nil {
		fail(err)
	}

	// Persist the report next to the memory layer.
	outDir := filepath.Join(cwd, ".niuniu-agent", "explore")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fail(err)
	}
	reportPath := filepath.Join(outDir, "explore-report-"+time.Now().UTC().Format("20060102-150405")+".md")
	var b strings.Builder
	fmt.Fprintf(&b, "# RSI explore report\n\n- Generated: %s\n- Memory freeze: %v\n- Pass rate: %.0f%%\n- Lessons distilled (verified passes only): %d\n\n",
		time.Now().Format(time.RFC3339), freeze, report.PassRate(), len(report.Lessons))
	fmt.Fprintf(&b, "## Broad phase (%d tasks)\n\n", len(report.Broad))
	for _, a := range report.Broad {
		mark := "❌"
		if a.Pass {
			mark = "✅ lesson: " + a.LessonID
		}
		fmt.Fprintf(&b, "- %s %s %s\n", mark, a.TaskName, a.FailError)
	}
	fmt.Fprintf(&b, "\n## Deep phase (%d tasks — focused on failed areas)\n\n", len(report.Deep))
	for _, a := range report.Deep {
		mark := "❌"
		if a.Pass {
			mark = "✅ lesson: " + a.LessonID
		}
		fmt.Fprintf(&b, "- %s %s %s\n", mark, a.TaskName, a.FailError)
	}
	if err := os.WriteFile(reportPath, []byte(b.String()), 0o644); err != nil {
		fail(err)
	}

	fmt.Printf("explore done: pass rate %.0f%%, lessons distilled %d\n  report: %s\n",
		report.PassRate(), len(report.Lessons), reportPath)
	fmt.Println("Effect loop: re-run `niuniu-agent eval -baseline <summary.json>` to compare pass rate with/without the distilled lessons.")
}

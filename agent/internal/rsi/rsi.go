// Package rsi implements niuniu-agent's recursive self-improvement warm-up
// ("explore"): a Curriculum generates practice tasks for the workspace's
// task family, an Actor attempts them in eval sandboxes, and a Verifier
// (rule checks — no model, no access to the Actor's reasoning or memory)
// judges them. ONLY verified-pass attempts distill durable lessons into
// project memory, which directly counters the known "memory keeps wrong
// rules" failure mode of naive RSI loops.
//
// Method inspired by RSIAgent (AetherLabsAI, Apache-2.0,
// arXiv:2609.15364) — idea-level reuse with attribution, clean-room code.
package rsi

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/eval"
	"github.com/niuniu-dev/niuniu/agent/internal/loop"
	"github.com/niuniu-dev/niuniu/agent/internal/memory"
	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// Options configures one explore run.
type Options struct {
	// Broad is the number of diverse practice tasks generated first.
	Broad int
	// Deep is the number of FOCUS tasks generated afterwards, aimed at the
	// weak spots (failed task descriptions) from the broad phase.
	Deep int
	// PerTaskTimeout bounds one Actor attempt. 0 → 2 minutes.
	PerTaskTimeout time.Duration
	// MemoryFreeze (default true) removes memory-writing tools from the
	// Actor: lessons are distilled by the harness, only from VERIFIED
	// passes — a failing Actor cannot poison memory.
	MemoryFreeze bool
}

// Attempt is one practice task's outcome.
type Attempt struct {
	TaskName  string
	Pass      bool
	LessonID  string // set only for verified passes that distilled a lesson
	FailError string
}

// Report summarizes one explore run.
type Report struct {
	Broad    []Attempt
	Deep     []Attempt
	Lessons  []string
	Duration time.Duration
}

// PassRate returns verified-pass percentage over all attempts.
func (r *Report) PassRate() float64 {
	total := len(r.Broad) + len(r.Deep)
	if total == 0 {
		return 0
	}
	pass := 0
	for _, a := range append(append([]Attempt{}, r.Broad...), r.Deep...) {
		if a.Pass {
			pass++
		}
	}
	return float64(pass) / float64(total) * 100
}

// curriculumPrompt asks the model to author practice tasks in eval task
// markdown format (self-play curriculum: no human task authoring).
func curriculumPrompt(cwd string, n int, focus string) string {
	b := &strings.Builder{}
	fmt.Fprintf(b, `You are designing %d practice tasks to warm up a coding agent for THIS workspace (working directory: %s). Inspect nothing — invent tasks TYPICAL for the project family you can infer from the directory name and general software practice.

%s`, n, cwd, focus)
	b.WriteString(`
Output rules — follow EXACTLY:
- One task per block; blocks separated by a line containing only ====
- Each block is a task markdown document:
---
name: rsi-<kebab-name>
description: one line
---
## Task
<complete, self-contained instruction>
## Fixtures
- <file>: <content, single line; use \n escapes for newlines>
## Checks
- contains: <file>, <expected text>
- file-exists: <file>
Requirements: deterministic rule-checkable outcomes; the Fixtures must make the task solvable without network access; Checks must pass iff the task was done correctly. Do not reference any real repository.`)
	return b.String()
}

// splitCurriculum breaks the model output into individual task documents.
func splitCurriculum(out string) []string {
	var docs []string
	for _, block := range strings.Split(out, "\n====") {
		block = strings.TrimSpace(block)
		if strings.HasPrefix(block, "====") {
			block = strings.TrimSpace(strings.TrimPrefix(block, "===="))
		}
		if strings.HasPrefix(block, "---\n") {
			docs = append(docs, block)
		}
	}
	return docs
}

// distillPrompt extracts one durable lesson from a VERIFIED attempt.
func distillPrompt(taskDoc, output string) string {
	return fmt.Sprintf("A coding agent completed this practice task:\n\nTASK:\n%s\n\nAGENT OUTPUT:\n%s\n\nDistill ONE reusable lesson (a technique, pitfall, or convention worth remembering for similar tasks in this workspace). Output exactly:\nTITLE: <short kebab title>\nTYPE: <pattern|gotcha>\n---\n<one or two sentences>", taskDoc, output)
}

// Runner wires the three roles.
type Runner struct {
	Model    model.Model
	Lessons  []string        // distilled lesson ids from verified passes
	Reg      *tools.Registry // Actor toolset (already excludes memory tools when frozen)
	Store    *memory.Store   // verified lessons land here (project layer)
	Cwd      string
	Opts     Options
	memTools []tools.Tool // removed under freeze, restored after
}

// Run executes broad → deep phases. The Verifier is eval.RunChecks (pure
// rule execution — structurally firewalled: it receives only the task's
// checks and the sandbox directory, never the Actor's transcript or
// memory).
func (r *Runner) Run(ctx context.Context) (*Report, error) {
	report := &Report{}
	start := time.Now()
	defer func() { report.Duration = time.Since(start) }()

	// Memory freeze: strip memory-writing tools from the Actor set.
	r.freezeMemoryTools()

	weakSpots := ""
	phases := []struct {
		name  string
		count int
	}{}
	if r.Opts.Broad > 0 {
		phases = append(phases, struct {
			name  string
			count int
		}{"broad", r.Opts.Broad})
	}
	if r.Opts.Deep > 0 {
		phases = append(phases, struct {
			name  string
			count int
		}{"deep", r.Opts.Deep})
	}
	for _, phase := range phases {
		focus := ""
		if phase.name == "deep" {
			focus = "FOCUS: generate tasks that specifically exercise these previously-FAILED areas:\n" + weakSpots
		}
		resp, err := r.Model.Complete(ctx, model.Request{
			System: "You are a curriculum designer for coding-agent practice tasks.",
			Messages: []model.Message{{Role: model.RoleUser, Blocks: []model.Block{
				{Type: model.BlockText, Text: curriculumPrompt(r.Cwd, phase.count, focus)},
			}}},
			// Large budget: on gateways with always-on thinking the
			// reasoning shares this budget, so the text needs headroom.
			MaxTokens: 16384,
		})
		if err != nil {
			return report, fmt.Errorf("curriculum (%s): %w", phase.name, err)
		}
		raw := resp.Message.Text()
		log.Printf("rsi: curriculum raw head (%d bytes): %.300s", len(raw), raw)
		docs := splitCurriculum(raw)
		var out *[]Attempt
		if phase.name == "broad" {
			out = &report.Broad
		} else {
			out = &report.Deep
		}
		var failures []string
		for i, doc := range docs {
			task, err := eval.ParseTask(doc)
			if err != nil {
				head := doc
				if len(head) > 400 {
					head = head[:400]
				}
				log.Printf("rsi: skip malformed curriculum item %d: %v\n--- doc head ---\n%s", i+1, err, head)
				continue
			}
			att := r.attempt(ctx, task, fmt.Sprintf("%s-%d", phase.name, i+1))
			*out = append(*out, att)
			if !att.Pass {
				failures = append(failures, "- "+task.Prompt+" (error: "+att.FailError+")")
			}
		}
		weakSpots = strings.Join(failures, "\n")
		// Task-aware tiered recall: the deep-phase curriculum gets the
		// lessons most relevant to the failures (not a blanket dump).
		if recall, rerr := r.Store.RecallFor(weakSpots, 3, 1024); rerr == nil && recall != "" {
			weakSpots += "\nRelevant prior lessons:\n" + recall
		}
		if weakSpots == "" {
			weakSpots = "(none — all broad attempts passed; deepen the same task family with harder variants)"
		}
	}
	report.Lessons = r.Lessons
	return report, nil
}

// freezeMemoryTools removes memory-writing tools from the registry
// (executed memory freeze) and remembers them for restoration.
func (r *Runner) freezeMemoryTools() {
	if !r.Opts.MemoryFreeze {
		return
	}
	for _, name := range []string{"MemorySave", "MemoryConsolidate"} {
		if t, ok := r.Reg.Lookup(name); ok {
			r.memTools = append(r.memTools, t)
			r.Reg.Remove(name)
		}
	}
}

// attempt runs one Actor attempt + Verifier, and distills a lesson only
// from verified passes.
func (r *Runner) attempt(ctx context.Context, task *eval.Task, label string) Attempt {
	att := Attempt{TaskName: task.Name}
	timeout := r.Opts.PerTaskTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	actCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dir, err := task.PrepareSandbox()
	if err != nil {
		att.FailError = "sandbox: " + err.Error()
		return att
	}
	defer os.RemoveAll(dir)

	prevWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		att.FailError = "chdir: " + err.Error()
		return att
	}
	defer os.Chdir(prevWd)

	sess := loop.NewSession(r.Model, r.Reg, "You are niuniu-agent in a practice sandbox. Complete the task using the tools.")
	res, perr := sess.Prompt(actCtx, task.Prompt, loop.Options{Perms: newAllPerms()})
	if perr != nil {
		att.FailError = perr.Error()
		return att
	}

	// Verifier: pure rule execution over the sandbox. Firewalled by
	// construction — no model call, no Actor transcript/memory access.
	checks := eval.RunChecks(*task, dir, res.Text)
	for _, c := range checks {
		if !c.Pass {
			att.FailError = "check failed: " + c.Detail
			return att
		}
	}
	att.Pass = true

	// Verified pass → distill ONE grounded lesson into project memory.
	dresp, derr := r.Model.Complete(ctx, model.Request{
		System: "You distill reusable coding lessons.",
		Messages: []model.Message{{Role: model.RoleUser, Blocks: []model.Block{
			{Type: model.BlockText, Text: distillPrompt(taskDocOf(task), res.Text)},
		}}},
		MaxTokens: 2048, // headroom for always-on-thinking gateways
	})
	if derr != nil {
		return att // lesson distillation is best-effort; pass stands
	}
	lesson := strings.TrimSpace(dresp.Message.Text())
	title, body, perr2 := parseLesson(lesson)
	if perr2 != nil {
		return att
	}
	id, serr := r.Store.Save(memory.Entry{
		Title: "rsi-" + title, Type: memory.TypePattern,
		Content: body, Tags: []string{"rsi"},
	})
	if serr == nil {
		att.LessonID = id
		r.Lessons = append(r.Lessons, id)
	}
	return att
}

func taskDocOf(t *eval.Task) string {
	return fmt.Sprintf("name: %s\ntask: %s", t.Name, t.Prompt)
}

// parseLesson parses the TITLE/TYPE/---/body lesson format.
func parseLesson(text string) (title, body string, err error) {
	rest, ok := strings.CutPrefix(text, "TITLE:")
	if !ok {
		return "", "", fmt.Errorf("no TITLE")
	}
	title, rest, _ = strings.Cut(rest, "\n")
	title = strings.TrimSpace(title)
	rest = strings.TrimSpace(rest)
	typeLine, rest, ok := strings.Cut(rest, "\n")
	if !ok || !strings.HasPrefix(typeLine, "TYPE:") {
		return "", "", fmt.Errorf("no TYPE")
	}
	_, body, _ = strings.Cut(rest, "---")
	body = strings.TrimSpace(body)
	if title == "" || body == "" {
		return "", "", fmt.Errorf("empty lesson")
	}
	return title, body, nil
}

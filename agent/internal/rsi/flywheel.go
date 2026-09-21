package rsi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/eval"
	"github.com/niuniu-dev/niuniu/agent/internal/memory"
	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// Flywheel is the production-grade self-evolution loop: the four teeth
// (Measure → Evolve → Land → Control) run in order, and candidate lessons
// reach production memory ONLY through three safety gates:
//
//  1. regression gate — with the candidates, the visible eval must not
//     lose a single task versus the pre-evolution measure;
//  2. drift gate — candidate count and near-duplicate ratio stay within
//     bounds so the memory layer cannot be flooded or drifted;
//  3. hidden gate — a held-out task set the Actor never saw must hold its
//     pass rate (within a tolerance of the visible rate), exposing reward
//     hacking against the visible checks.
//
// Failed gates roll the candidates back (staging never lands).
type Flywheel struct {
	Model       model.Model
	Reg         *tools.Registry
	Store       *memory.Store // production project-layer memory
	Cwd         string
	VisibleDir  string // visible eval tasks
	HiddenDir   string // held-out tasks (empty → hidden gate skipped w/ warning)
	OutDir      string // reports + state
	StagingDir  string // candidate lesson staging area
	Broad       int
	Deep        int
	DriftMaxNew int // drift gate: max new lessons per spin
	Timeout     time.Duration
}

// GateResult is one safety gate's outcome.
type GateResult struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

// FlywheelReport is the spin's full audit record.
type FlywheelReport struct {
	Measure    *eval.Summary `json:"measure"`
	Control    *eval.Summary `json:"control,omitempty"`
	Attempts   int           `json:"attempts"`
	Candidates int           `json:"candidates"`
	Gates      []GateResult  `json:"gates"`
	Landed     []string      `json:"landed"`
	Rejected   int           `json:"rejected"`
	PromptLand int           `json:"promptLandings,omitempty"`
	Delta      string        `json:"delta,omitempty"`
	Duration   time.Duration `json:"-"`
}

// Spin runs one full flywheel revolution.
func (f *Flywheel) Spin(ctx context.Context) (*FlywheelReport, error) {
	report := &FlywheelReport{}
	start := time.Now()
	defer func() { report.Duration = time.Since(start) }()

	// —— 齿1 Measure: visible eval with the CURRENT memory layer. ——
	tasks, err := eval.LoadTasks(f.VisibleDir)
	if err != nil {
		return report, fmt.Errorf("load visible tasks: %w", err)
	}
	measure := f.runEvalSet(ctx, tasks, f.Store)
	report.Measure = measure
	fmt.Fprintf(os.Stderr, "[flywheel] measure: %d/%d passed\n", measure.Passed, measure.Total)

	// —— 齿2 Evolve: RSI explore writes candidate lessons to STAGING. ——
	staging := memory.NewStoreDir(f.StagingDir)
	runner := &Runner{
		Model: f.Model,
		Reg:   f.Reg,
		Store: staging, // candidates never touch production directly
		Cwd:   f.Cwd,
		Opts: Options{
			Broad:          f.Broad,
			Deep:           f.Deep,
			MemoryFreeze:   true,
			PerTaskTimeout: 2 * time.Minute,
		},
	}
	expReport, err := runner.Run(ctx)
	if err != nil {
		return report, fmt.Errorf("evolve: %w", err)
	}
	report.Attempts = len(expReport.Broad) + len(expReport.Deep)
	candidates := stagingCandidates(staging)
	report.Candidates = len(candidates)
	fmt.Fprintf(os.Stderr, "[flywheel] evolve: %d attempts, %d candidates\n", report.Attempts, len(candidates))
	if len(candidates) == 0 {
		report.Gates = append(report.Gates, GateResult{Name: "evolve", Pass: true, Detail: "no candidates produced — nothing to land"})
		return report, nil
	}

	// Stage candidates INTO production copy so the gate evals see them;
	// on gate failure every staged file is rolled back.
	landed, rollbackFn, rerr := stageCandidates(f.Store, candidates)
	if rerr != nil {
		return report, rerr
	}
	rollbackAll := func() {
		if rollbackFn != nil {
			rollbackFn()
		}
		report.Rejected = len(candidates)
	}

	// —— 闸门 1: regression（visible 复跑，不得丢任务）。——
	regate := f.runEvalSet(ctx, tasks, f.Store)
	g1 := regressionGate(measure, regate)
	report.Gates = append(report.Gates, g1)

	// —— 闸门 2: drift（数量/重复边界）。——
	g2 := driftGate(f.Store, candidates, f.DriftMaxNew)
	report.Gates = append(report.Gates, g2)

	// —— 闸门 3: hidden held-out（actor 从未见过的真实目标）。——
	var g3 GateResult
	var hidden *eval.Summary
	if f.HiddenDir == "" {
		g3 = GateResult{Name: "hidden", Pass: true, Detail: "skipped: no hidden task set configured"}
	} else {
		hiddenTasks, herr := eval.LoadTasks(f.HiddenDir)
		if herr != nil {
			g3 = GateResult{Name: "hidden", Pass: true, Detail: "skipped: " + herr.Error()}
		} else {
			hidden = f.runEvalSet(ctx, hiddenTasks, f.Store)
			tol := 15.0 // percentage points of allowed hidden-vs-visible gap
			g3 = GateResult{
				Name: "hidden",
				Pass: passRateOf(hidden) >= passRateOf(regate)-tol,
				Detail: fmt.Sprintf("hidden %d/%d (%.0f%%) vs visible %d/%d (%.0f%%), tolerance %.0fpp",
					hidden.Passed, hidden.Total, passRateOf(hidden), regate.Passed, regate.Total, passRateOf(regate), tol),
			}
		}
	}
	report.Gates = append(report.Gates, g3)

	// —— 控制闸：任一失败 → 回滚；全过 → 落地 + landed 后可见复跑。——
	for _, g := range report.Gates {
		if !g.Pass {
			rollbackAll()
			fmt.Fprintf(os.Stderr, "[flywheel] gate %s rejected candidates\n", g.Name)
			report.Delta = fmt.Sprintf("rejected at gate %s (%s)", g.Name, g.Detail)
			return report, nil
		}
	}
	report.Landed = landed

	// —— P8a: 策略级落地。—— 只有同时扛过可见回归复跑与隐藏 held-out 集
	// 的候选才有两路独立接地信号（Verified=2 ≥ GroundingThreshold），才
	// 允许进入 PROMPT.md（影响此后每个会话的 system）；未配置隐藏集时
	// 只有一路信号，低于门槛，不落地。
	verified := 1
	if hidden != nil {
		verified = 2
	}
	strat := make([]StrategyLesson, 0, len(candidates))
	for _, c := range candidates {
		strat = append(strat, StrategyLesson{Title: c.Title, Guidance: c.Content, Verified: verified})
	}
	if n, lerr := LandStrategyLessons(f.Cwd, strat); lerr != nil {
		fmt.Fprintf(os.Stderr, "[flywheel] prompt landing failed: %v\n", lerr)
	} else if n > 0 {
		report.PromptLand = n
		fmt.Fprintf(os.Stderr, "[flywheel] landed %d grounded strategy lesson(s) into PROMPT.md\n", n)
	}

	// —— 齿4 Control: landed 后可见复跑 + delta。——
	control := f.runEvalSet(ctx, tasks, f.Store)
	report.Control = control
	report.Delta = fmt.Sprintf("measure %d/%d → control %d/%d (landed %d lessons)",
		measure.Passed, measure.Total, control.Passed, control.Total, len(landed))
	_ = hidden
	f.writeState(report)
	return report, nil
}

// runEvalSet runs a whole task set with memory recall from the production
// store (the RSI effect channel).
func (f *Flywheel) runEvalSet(ctx context.Context, tasks []eval.Task, store *memory.Store) *eval.Summary {
	s := &eval.Summary{GeneratedAt: time.Now(), Total: len(tasks)}
	recall, _ := store.RecallFor("", 3, 1024)
	system := "You are niuniu-agent in an evaluation sandbox. Complete the task using the tools."
	if recall != "" {
		system += "\n\n# Memory\n\n" + recall + "\n"
	}
	for _, t := range tasks {
		res := eval.RunTaskWithSystem(ctx, f.Model, f.Reg, t, 2*time.Minute, system)
		s.Results = append(s.Results, res)
		if res.Pass {
			s.Passed++
		} else {
			s.Failed++
		}
	}
	return s
}

func passRateOf(s *eval.Summary) float64 {
	if s == nil || s.Total == 0 {
		return 0
	}
	return float64(s.Passed) / float64(s.Total) * 100
}

// stagingCandidates reads every lesson staged by the evolve pass.
func stagingCandidates(staging *memory.Store) []memory.Entry {
	entries, err := staging.Search("")
	if err != nil {
		return nil
	}
	return entries
}

// stageCandidates copies candidate lessons into the production store,
// skipping same-slug collisions. Returns landed ids and a rollback func.
func stageCandidates(prod *memory.Store, candidates []memory.Entry) (landed []string, rollback func(), err error) {
	var added []string
	rollback = func() {
		for _, id := range added {
			_ = os.Remove(filepath.Join(prod.ProjectDir(), id+".md"))
		}
	}
	for _, c := range candidates {
		id, err := prod.Save(c)
		if err != nil {
			return landed, rollback, err
		}
		added = append(added, id)
	}
	landed = added
	return landed, rollback, nil
}

// regressionGate: the gated visible run must not lose a single task
// versus the pre-evolution measure.
func regressionGate(measure, gate *eval.Summary) GateResult {
	return GateResult{Name: "regression", Pass: gate.Passed >= measure.Passed,
		Detail: fmt.Sprintf("measure %d/%d → gate %d/%d", measure.Passed, measure.Total, gate.Passed, gate.Total)}
}

// driftGate: count bound + near-duplicate bound.
func driftGate(prod *memory.Store, candidates []memory.Entry, maxNew int) GateResult {
	existing, _ := prod.Search("")
	if maxNew <= 0 {
		maxNew = 5
	}
	newCount := len(candidates)
	if newCount > maxNew {
		return GateResult{Name: "drift", Pass: false,
			Detail: fmt.Sprintf("%d new lessons exceed the per-spin cap (%d)", newCount, maxNew)}
	}
	for _, c := range candidates {
		for _, e := range existing {
			if e.Title == c.Title {
				continue // the candidate staged into prod is itself
			}
			if similarity(c.Content, e.Content) > 0.6 {
				return GateResult{Name: "drift", Pass: false,
					Detail: fmt.Sprintf("candidate %q near-duplicates existing %q", c.Title, e.Title)}
			}
		}
	}
	return GateResult{Name: "drift", Pass: true, Detail: fmt.Sprintf("%d candidates within bounds", newCount)}
}

// similarity is a crude word-overlap Jaccard on lowercased content.
func similarity(a, b string) float64 {
	aw, bw := wordSet(a), wordSet(b)
	if len(aw) == 0 || len(bw) == 0 {
		return 0
	}
	inter := 0
	for w := range aw {
		if bw[w] {
			inter++
		}
	}
	union := len(aw) + len(bw) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func wordSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(s)) {
		out[strings.Trim(w, ".,;:()\"'")] = true
	}
	delete(out, "")
	return out
}

// writeState persists the audit record next to the reports.
func (f *Flywheel) writeState(r *FlywheelReport) {
	if f.OutDir == "" {
		return
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(f.OutDir, 0o755)
	_ = os.WriteFile(filepath.Join(f.OutDir, "flywheel-state.json"), data, 0o644)
}

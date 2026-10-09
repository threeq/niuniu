// Package eval implements niuniu-agent's evaluation harness: markdown task
// definitions (task prompt + sandbox fixtures + deterministic checks), a
// runner that executes each task in a throwaway temp directory, and
// JSON/markdown summaries comparable against a saved baseline.
//
// Safety posture: tasks run in a fresh per-task sandbox (never the real
// repo), task corpora are sanitized before they land in eval/tasks/, and
// judging is rule-based by default — no external LLM, no data leaves the
// machine.
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/loop"
	"github.com/niuniu-dev/niuniu/agent/internal/memory"
	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/perm"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// Task is one eval case parsed from eval/tasks/<name>.md.
type Task struct {
	Name        string
	Description string
	Prompt      string            // instruction given to the agent
	Fixtures    map[string]string // sandbox file → content (written before the run)
	Checks      []Check           // deterministic pass/fail assertions
}

// Check is one rule assertion.
type Check struct {
	Kind  string // contains | not-contains | file-exists | command-exit-0 | output-contains
	File  string // file the check applies to
	Value string // expected/forbidden text, or the command
}

// CheckResult is the outcome of one rule assertion.
type CheckResult struct {
	Check  Check  `json:"check"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

// Result is one task run's outcome.
type Result struct {
	Name     string        `json:"name"`
	Pass     bool          `json:"pass"`
	Error    string        `json:"error,omitempty"`
	Checks   []CheckResult `json:"checks,omitempty"`
	Rounds   int           `json:"rounds"`
	Duration time.Duration `json:"-"`
	// DurationMS is the wall time in milliseconds (time.Duration serializes
	// as nanoseconds — misleading under the "duration_ms" key).
	DurationMS int64       `json:"duration_ms"`
	Usage      model.Usage `json:"usage"`
	Output     string      `json:"output,omitempty"`
}

// Summary aggregates a batch run.
type Summary struct {
	GeneratedAt   time.Time `json:"generated_at"`
	Total         int       `json:"total"`
	Passed        int       `json:"passed"`
	Failed        int       `json:"failed"`
	TotalDuration string    `json:"total_duration"`
	TotalUsage    struct {
		Input     int64 `json:"input"`
		Output    int64 `json:"output"`
		CacheRead int64 `json:"cache_read"`
	} `json:"total_usage"`
	Results []Result `json:"results"`
}

// ParseTask parses one task markdown document (exposed for the RSI
// curriculum, which generates task documents in the same format).
func ParseTask(text string) (*Task, error) { return parseTask(text) }

// LoadTasks parses every *.md under dir (sorted by filename).
func LoadTasks(dir string) ([]Task, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read tasks dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var out []Task
	for _, n := range names {
		data, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return nil, err
		}
		t, err := parseTask(string(data))
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", n, err)
		}
		if t.Name == "" {
			t.Name = strings.TrimSuffix(n, ".md")
		}
		out = append(out, *t)
	}
	return out, nil
}

// parseTask parses the task markdown format:
//
//	---
//	name: …
//	description: …
//	---
//	## Task
//	<prompt>
//	## Fixtures
//	- path: single-line-content
//	## Checks
//	- contains: file, text
//	- file-exists: path
//	- command-exit-0: cmd
//	- output-contains: text
func parseTask(text string) (*Task, error) {
	t := &Task{Fixtures: map[string]string{}}
	// Windows checkouts (core.autocrlf) hand us CRLF bytes; the "---\n"
	// prefix match and section headers are LF-anchored, so normalize first
	// so frontmatter/section parsing stays checkout-independent.
	text = strings.ReplaceAll(text, "\r\n", "\n")
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return nil, fmt.Errorf("missing frontmatter")
	}
	fm, rest, found := strings.Cut(rest, "\n---")
	if !found {
		return nil, fmt.Errorf("unterminated frontmatter")
	}
	for _, line := range strings.Split(fm, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "name":
			t.Name = val
		case "description":
			t.Description = val
		}
	}

	section := ""
	curFixture := ""
	for _, line := range strings.Split(rest, "\n") {
		trimmed := strings.TrimSpace(line)
		switch trimmed {
		case "## Task":
			section = "task"
			continue
		case "## Fixtures":
			section = "fixtures"
			curFixture = ""
			continue
		case "## Checks":
			section = "checks"
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "<!--") {
			continue
		}
		switch section {
		case "task":
			if t.Prompt != "" {
				t.Prompt += "\n"
			}
			t.Prompt += trimmed
		case "fixtures":
			if strings.HasPrefix(trimmed, "- ") {
				arg, val, ok := strings.Cut(trimmed[2:], ":")
				if !ok {
					return nil, fmt.Errorf("bad fixture line %q (want \"- path: content\")", trimmed)
				}
				curFixture = strings.TrimSpace(arg)
				t.Fixtures[curFixture] = unescapeNL(strings.TrimSpace(val))
			} else if curFixture != "" {
				// Continuation line: multi-line fixture content.
				t.Fixtures[curFixture] += "\n" + unescapeNL(trimmed)
			}
		case "checks":
			if !strings.HasPrefix(trimmed, "- ") {
				// Continuation line: multi-line expected value rides the
				// previous check.
				if len(t.Checks) > 0 {
					t.Checks[len(t.Checks)-1].Value += "\n" + unescapeNL(trimmed)
				}
				continue
			}
			spec := strings.TrimPrefix(trimmed, "- ")
			kind, rest2, ok := strings.Cut(spec, ":")
			if !ok {
				return nil, fmt.Errorf("bad check line %q", trimmed)
			}
			chk := Check{Kind: strings.TrimSpace(kind)}
			rest2 = strings.TrimSpace(rest2)
			switch chk.Kind {
			case "contains", "not-contains":
				f, v, _ := strings.Cut(rest2, ",")
				// Strip ONE leading space, never TrimSpace: the expected
				// value may legitimately start with whitespace (a tab).
				chk.File = unescapeNL(strings.TrimSpace(f))
				chk.Value = unescapeNL(strings.TrimPrefix(v, " "))
			case "file-exists", "command-exit-0", "output-contains":
				chk.Value = rest2
			default:
				return nil, fmt.Errorf("unknown check kind %q", chk.Kind)
			}
			t.Checks = append(t.Checks, chk)
		}
	}
	if t.Prompt == "" {
		return nil, fmt.Errorf("empty ## Task")
	}
	if len(t.Checks) == 0 {
		return nil, fmt.Errorf("no ## Checks")
	}
	return t, nil
}

// unescapeNL converts literal backslash-n sequences in task markdown into
// real newlines, so multi-line fixtures and checks stay one logical line
// in the markdown source.
func unescapeNL(s string) string {
	s = strings.ReplaceAll(s, `\n`, "\n")
	return strings.ReplaceAll(s, `\t`, "\t")
}

// PrepareSandbox creates a fresh temp dir and lays down the fixtures.
// The caller owns cleanup.
func (t *Task) PrepareSandbox() (string, error) {
	dir, err := os.MkdirTemp("", "niuniu-eval-")
	if err != nil {
		return "", err
	}
	for rel, content := range t.Fixtures {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return dir, err
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return dir, err
		}
	}
	return dir, nil
}

// RunTask executes one task in a fresh sandbox: run the agent loop, then
// judge with the rule checks. memoryDir (optional) injects recalled
// long-term memory from that layer into the task system prompt — the RSI
// effect channel (explore-distilled lessons reach the evaluated runs).
// The sandbox is removed before returning.
func RunTask(ctx context.Context, m model.Model, reg *tools.Registry, t Task, timeout time.Duration, memoryDir string) Result {
	res := Result{Name: t.Name}
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dir, err := t.PrepareSandbox()
	if err != nil {
		res.Error = fmt.Sprintf("sandbox: %v", err)
		return res
	}
	defer os.RemoveAll(dir)

	prevWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		res.Error = fmt.Sprintf("chdir: %v", err)
		return res
	}
	defer os.Chdir(prevWd)

	system := "You are niuniu-agent in an evaluation sandbox. Complete the task using the tools."
	if memoryDir != "" {
		store := memory.NewStoreDir(memoryDir)
		recall, rerr := store.Recall(5, 2048)
		if rerr != nil {
			recall = ""
		}
		system += memory.Section(recall)
	}
	start := time.Now()
	sess := loop.NewSession(m, reg, system)
	out, err := sess.Prompt(ctx, t.Prompt, loop.Options{Perms: perm.NewPolicy(true)})
	res.Duration = time.Since(start)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Rounds = out.Rounds
	res.Usage = out.Usage
	res.DurationMS = res.Duration.Milliseconds()
	res.Output = out.Text

	res.Checks = RunChecks(t, dir, out.Text)
	for _, c := range res.Checks {
		if !c.Pass {
			res.Error = "check failed: " + c.Detail
			return res
		}
	}
	res.Pass = true
	return res
}

// RunTaskWithSystem is RunTask with an explicit system prompt (the RSI
// flywheel injects recalled memory here).
func RunTaskWithSystem(ctx context.Context, m model.Model, reg *tools.Registry, t Task, timeout time.Duration, system string) Result {
	sess := loop.NewSession(m, reg, system)
	res := Result{Name: t.Name}
	dir, err := t.PrepareSandbox()
	if err != nil {
		res.Error = "sandbox: " + err.Error()
		return res
	}
	defer os.RemoveAll(dir)
	prevWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		res.Error = "chdir: " + err.Error()
		return res
	}
	defer os.Chdir(prevWd)
	start := time.Now()
	out, perr := sess.Prompt(ctx, t.Prompt, loop.Options{Perms: perm.NewPolicy(true)})
	res.Duration = time.Since(start)
	if perr != nil {
		res.Error = perr.Error()
		return res
	}
	res.Rounds = out.Rounds
	res.Usage = out.Usage
	res.Output = out.Text
	res.Checks = RunChecks(t, dir, out.Text)
	res.Pass = true
	for _, c := range res.Checks {
		if !c.Pass {
			res.Pass = false
			res.Error = "check failed: " + c.Detail
			break
		}
	}
	return res
}

// RunSetWithSystem runs a whole task set under one fixed system prompt and
// aggregates the results (used by the self-evolution harness to measure a
// candidate system prompt against the public/private task sets).
func RunSetWithSystem(ctx context.Context, m model.Model, reg *tools.Registry, tasks []Task, timeout time.Duration, system string) Summary {
	sum := Summary{Total: len(tasks)}
	for _, t := range tasks {
		r := RunTaskWithSystem(ctx, m, reg, t, timeout, system)
		sum.Results = append(sum.Results, r)
		if r.Pass {
			sum.Passed++
		} else {
			sum.Failed++
		}
	}
	return sum
}

// RunChecks judges every rule against the sandbox (and the agent output).
func RunChecks(t Task, dir, output string) []CheckResult {
	var out []CheckResult
	for _, chk := range t.Checks {
		cr := CheckResult{Check: chk}
		switch chk.Kind {
		case "file-exists":
			_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(chk.Value)))
			cr.Pass = err == nil
			if !cr.Pass {
				cr.Detail = "file not found: " + chk.Value
			}
		case "contains", "not-contains":
			data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(chk.File)))
			if err != nil {
				cr.Pass = false
				cr.Detail = "read " + chk.File + ": " + err.Error()
			} else {
				has := strings.Contains(string(data), chk.Value)
				cr.Pass = (chk.Kind == "contains") == has
				if !cr.Pass {
					cr.Detail = fmt.Sprintf("%s %q in %s", chk.Kind, chk.Value, chk.File)
				}
			}
		case "output-contains":
			cr.Pass = strings.Contains(output, chk.Value)
			if !cr.Pass {
				cr.Detail = "output missing " + chk.Value
			}
		case "command-exit-0":
			cmd := exec.Command(sandboxShellName(), sandboxShellArg(chk.Value)...)
			cmd.Dir = dir
			out2, err := cmd.CombinedOutput()
			if err != nil {
				cr.Pass = false
				cr.Detail = fmt.Sprintf("command %q: %v (%s)", chk.Value, err, strings.TrimSpace(string(out2)))
			} else {
				cr.Pass = true
			}
		}
		out = append(out, cr)
	}
	return out
}

// WriteJSON persists the summary for baseline comparison.
func (s Summary) WriteJSON(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// WriteMarkdown renders a human-readable report.
func (s Summary) WriteMarkdown(path string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# niuniu-agent eval report\n\n- Generated: %s\n- Tasks: %d, passed: %d, failed: %d (%.0f%% pass rate)\n- Total duration: %s\n- Tokens: input %d / output %d / cache-read %d\n\n",
		s.GeneratedAt.Format(time.RFC3339), s.Total, s.Passed, s.Failed,
		passRate(s), s.TotalDuration,
		s.TotalUsage.Input, s.TotalUsage.Output, s.TotalUsage.CacheRead)
	b.WriteString("| Task | Pass | Rounds | Duration | In | Out | Note |\n|---|---|---|---|---|---|---|\n")
	for _, r := range s.Results {
		status := "✅"
		note := ""
		if !r.Pass {
			status = "❌"
			note = r.Error
		}
		fmt.Fprintf(&b, "| %s | %s | %d | %s | %d | %d | %s |\n",
			r.Name, status, r.Rounds, r.Duration.Round(time.Millisecond),
			r.Usage.InputTokens, r.Usage.OutputTokens, strings.ReplaceAll(note, "|", "/"))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func passRate(s Summary) float64 {
	if s.Total == 0 {
		return 0
	}
	return float64(s.Passed) / float64(s.Total) * 100
}

// CompareAgainst renders the delta versus a saved baseline summary.
func (s Summary) CompareAgainst(baselinePath string) (string, error) {
	data, err := os.ReadFile(baselinePath)
	if err != nil {
		return "", err
	}
	var base Summary
	if err := json.Unmarshal(data, &base); err != nil {
		return "", fmt.Errorf("parse baseline: %w", err)
	}
	basePass := passRate(base)
	curPass := passRate(s)
	line := fmt.Sprintf("baseline: %d/%d passed (%.0f%%), %s; current: %d/%d passed (%.0f%%), %s — pass rate delta %+.0f%%",
		base.Passed, base.Total, basePass, base.TotalDuration,
		s.Passed, s.Total, curPass, s.TotalDuration, curPass-basePass)
	return line, nil
}

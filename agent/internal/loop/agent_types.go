package loop

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// AgentType is a named subagent preset: role preamble, tool whitelist and
// an optional model tier. Sharing/isolation semantics from P5 are
// unchanged — a typed subagent is still an independent Session.
type AgentType struct {
	Name        string
	Description string
	// Preamble is appended to the child system prompt after the role header.
	Preamble string
	// Tools is the ALLOWLIST; empty = inherit the parent's child registry.
	Tools []string
	// ModelTier selects a model via AgentFactory.ModelFor ("" = default).
	ModelTier string
}

// BuiltinAgentTypes are the four stock presets.
func BuiltinAgentTypes() []AgentType {
	return []AgentType{
		{
			Name:        "explore",
			Description: "Read-only investigation: find code, trace behavior, report findings. Cannot modify files.",
			Preamble:    "You are in EXPLORE mode: investigate the codebase and report findings. Read-only — never modify files.",
			Tools:       []string{"LS", "Read", "Grep", "Glob"},
		},
		{
			Name:        "plan",
			Description: "Design an implementation plan: read the code, lay out ordered steps, surface risks. Cannot modify files.",
			Preamble:    "You are in PLAN mode: produce a concrete, ordered implementation plan (files to touch, steps, risks). Read-only.",
			Tools:       []string{"LS", "Read", "Grep", "Glob", "TodoWrite", "WebFetch"},
		},
		{
			Name:        "worker",
			Description: "Carry out implementation: write and edit code, run commands, verify the result.",
			Preamble:    "You are in WORKER mode: implement the given task with the available tools and verify your work.",
		},
		{
			Name:        "reviewer",
			Description: "Review a change for correctness and style: read code and diffs, report issues. Cannot modify files.",
			Preamble:    "You are in REVIEW mode: examine the code/diff described in the task and report concrete issues (severity + location). Read-only.",
			Tools:       []string{"LS", "Read", "Grep", "Glob"},
		},
	}
}

// LoadAgentTypes reads declarative custom types from dir
// (<cwd>/.niuniu-agent/agents/*.md): frontmatter carries name/description/
// tools/model, the markdown body is the preamble. Malformed files are
// skipped, never fatal.
func LoadAgentTypes(dir string) []AgentType {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []AgentType
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		at := parseAgentTypeDoc(string(data))
		if at == nil || at.Name == "" {
			continue
		}
		out = append(out, *at)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func parseAgentTypeDoc(text string) *AgentType {
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return nil // frontmatter required (name is the minimum)
	}
	fm, body, found := strings.Cut(rest, "\n---")
	if !found {
		return nil
	}
	at := &AgentType{Preamble: strings.TrimSpace(body)}
	for _, line := range strings.Split(fm, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "name":
			at.Name = val
		case "description":
			at.Description = val
		case "tools":
			for _, t := range strings.FieldsFunc(val, func(r rune) bool { return r == ',' || r == ' ' }) {
				if t != "" {
					at.Tools = append(at.Tools, t)
				}
			}
		case "model":
			at.ModelTier = val
		}
	}
	return at
}

// applyType customizes system/registry/model for the requested type.
// Returns an error naming the available types when unknown.
func (a *AgentFactory) applyType(reg *tools.Registry, system string, typeName string) (string, model.Model, error) {
	if typeName == "" {
		return system, a.Model, nil
	}
	types := a.Types
	if types == nil {
		types = BuiltinAgentTypes()
	}
	for _, at := range types {
		if at.Name != typeName {
			continue
		}
		if len(at.Tools) > 0 {
			allowed := map[string]bool{}
			for _, n := range at.Tools {
				allowed[n] = true
			}
			for _, def := range reg.Defs() {
				if !allowed[def.Name] {
					reg.Remove(def.Name)
				}
			}
		}
		m := a.Model
		if a.ModelFor != nil && at.ModelTier != "" {
			m = a.ModelFor(at.ModelTier)
		}
		return system + "\n\n# Subagent type: " + at.Name + "\n" + at.Preamble, m, nil
	}
	names := make([]string, 0, len(types))
	for _, at := range types {
		names = append(names, at.Name)
	}
	return "", nil, fmt.Errorf("unknown subagent_type %q (available: %s)", typeName, strings.Join(names, ", "))
}

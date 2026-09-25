// Package tools implements the agent's tool suite. P0 ships two Go-native
// tools (LS, Read) that work identically across Windows/macOS/Linux without
// a shell; the fuller suite (Write/Edit/Bash/Grep/Glob/…) arrives in P1.
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
)

// Tool is one capability the model can invoke. Execute receives the parsed
// schema-validated input and returns the result text fed back to the model
// (errors are reported to the model as error results, not fatal to the run).
type Tool interface {
	Def() model.ToolDef
	Execute(ctx context.Context, input json.RawMessage) (string, error)
}

// ImageResult is an optional interface for tools that can return images
// (Read on an image path): the loop prefers it and attaches the image
// blocks to the tool_result for vision-capable models.
type ImageResult interface {
	ExecuteWithImages(ctx context.Context, input json.RawMessage) (string, []model.Block, error)
}

// Registry maps tool names to implementations and holds the ToolDef list
// sent to the model.
type Registry struct {
	byName map[string]Tool
	defs   []model.ToolDef
}

// NewRegistry builds a registry from the given tools, in definition order.
func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{byName: make(map[string]Tool, len(tools))}
	for _, t := range tools {
		def := t.Def()
		r.byName[def.Name] = t
		r.defs = append(r.defs, def)
	}
	return r
}

// Defs returns the tool definitions to advertise to the model.
func (r *Registry) Defs() []model.ToolDef { return r.defs }

// Register adds one tool after construction (appended to the definition
// order). Re-registering a name replaces the implementation and keeps its
// original position.
func (r *Registry) Register(t Tool) {
	def := t.Def()
	if r.byName == nil {
		r.byName = make(map[string]Tool)
	}
	if _, exists := r.byName[def.Name]; !exists {
		r.defs = append(r.defs, def)
	} else {
		for i, d := range r.defs {
			if d.Name == def.Name {
				r.defs[i] = def
			}
		}
	}
	r.byName[def.Name] = t
}

// Lookup returns the named tool implementation (nil when absent).
func (r *Registry) Lookup(name string) (Tool, bool) {
	t, ok := r.byName[name]
	return t, ok
}

// Remove deletes a tool from the registry (used to enforce subagent
// tool-exclusion lists). Removing an unknown name is a no-op.
func (r *Registry) Remove(name string) {
	delete(r.byName, name)
	for i, d := range r.defs {
		if d.Name == name {
			r.defs = append(r.defs[:i], r.defs[i+1:]...)
			break
		}
	}
}

// Execute runs the named tool; unknown names are an error (which the loop
// turns into an error tool_result so the model can recover).
func (r *Registry) Execute(ctx context.Context, name string, input json.RawMessage) (string, error) {
	t, ok := r.byName[name]
	if !ok {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	return t.Execute(ctx, input)
}

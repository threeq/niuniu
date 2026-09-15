// Package loop implements the core agent loop: send the conversation to the
// model, execute any requested tools, feed the results back, and repeat
// until the model answers with text only — or the turn budget runs out.
package loop

import (
	"context"
	"fmt"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/perm"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// DefaultMaxTurns is the model round-trip budget when Options.MaxTurns is
// unset. Each turn may execute several tools.
const DefaultMaxTurns = 16

// EventKind identifies a progress event emitted while a Prompt is in flight.
type EventKind int

const (
	// EventText carries the assistant's final text for one model round.
	EventText EventKind = iota
	// EventToolStart fires before a tool executes.
	EventToolStart
	// EventToolEnd fires after a tool executes (IsError marks failure).
	EventToolEnd
)

// Event is one progress event; the ACP server maps these onto
// session/update notifications. Text is only set for EventText; ToolName/
// ToolID/ToolInput for start; ToolOutput for end.
type Event struct {
	Kind       EventKind
	Text       string
	ToolName   string
	ToolID     string
	ToolInput  string
	ToolOutput string
	IsError    bool
}

// Options bounds one Prompt.
type Options struct {
	MaxTurns int
	// Perms decides whether a tool may run; nil approves everything (the
	// caller — CLI main or the ACP server — owns policy).
	Perms perm.Checker
	// OnEvent receives progress events; nil disables.
	OnEvent func(Event)
}

// Session is a continuing conversation: successive Prompts accumulate
// message history so follow-up questions keep context. Safe for one
// in-flight Prompt at a time (hosts serialize).
type Session struct {
	m        model.Model
	reg      *tools.Registry
	system   string
	messages []model.Message
}

// NewSession starts a conversation with the given model, tools, and system
// prompt.
func NewSession(m model.Model, reg *tools.Registry, system string) *Session {
	return &Session{m: m, reg: reg, system: system}
}

// Prompt sends one user turn through the loop and returns the assistant's
// final text answer. Tool execution errors are reported back to the model as
// error tool_results, so a failing tool does not abort the run — the model
// can correct course. Denied tools (permission layer) likewise come back as
// error results.
func (s *Session) Prompt(ctx context.Context, userText string, opts Options) (string, error) {
	if opts.MaxTurns <= 0 {
		opts.MaxTurns = DefaultMaxTurns
	}
	if opts.Perms == nil {
		opts.Perms = perm.AllowAllChecker()
	}
	emit := func(e Event) {
		if opts.OnEvent != nil {
			opts.OnEvent(e)
		}
	}
	s.messages = append(s.messages, model.Message{
		Role:   model.RoleUser,
		Blocks: []model.Block{{Type: model.BlockText, Text: userText}},
	})
	for turn := 1; turn <= opts.MaxTurns; turn++ {
		resp, err := s.m.Complete(ctx, model.Request{
			System:   s.system,
			Messages: s.messages,
			Tools:    s.reg.Defs(),
		})
		if err != nil {
			return "", fmt.Errorf("model round %d: %w", turn, err)
		}
		s.messages = append(s.messages, resp.Message)

		uses := resp.Message.ToolUses()
		if len(uses) == 0 {
			text := resp.Message.Text()
			emit(Event{Kind: EventText, Text: text})
			return text, nil
		}

		results := make([]model.Block, 0, len(uses))
		for _, use := range uses {
			emit(Event{Kind: EventToolStart, ToolName: use.Name, ToolID: use.ID, ToolInput: string(use.Input)})

			decision := opts.Perms.Check(use.Name)
			if decision == perm.Ask {
				// No interactive escalation is wired in this context; treat
				// like a denial (the ACP server never uses a plain Ask).
				decision = perm.Deny
			}
			if decision == perm.Deny {
				results = append(results, model.Block{
					Type:      model.BlockToolResult,
					ToolUseID: use.ID,
					Text:      perm.DenyMessage,
					IsError:   true,
				})
				emit(Event{Kind: EventToolEnd, ToolName: use.Name, ToolID: use.ID, ToolOutput: perm.DenyMessage, IsError: true})
				continue
			}

			out, err := s.reg.Execute(ctx, use.Name, use.Input)
			if err != nil {
				results = append(results, model.Block{
					Type:      model.BlockToolResult,
					ToolUseID: use.ID,
					Text:      "ERROR: " + err.Error(),
					IsError:   true,
				})
				emit(Event{Kind: EventToolEnd, ToolName: use.Name, ToolID: use.ID, ToolOutput: err.Error(), IsError: true})
				continue
			}
			results = append(results, model.Block{
				Type:      model.BlockToolResult,
				ToolUseID: use.ID,
				Text:      out,
			})
			emit(Event{Kind: EventToolEnd, ToolName: use.Name, ToolID: use.ID, ToolOutput: out})
		}
		s.messages = append(s.messages, model.Message{Role: model.RoleUser, Blocks: results})
	}
	return "", fmt.Errorf("turn budget exhausted after %d rounds without a final answer", opts.MaxTurns)
}

// Run is the one-shot convenience form: a fresh Session driven by a single
// prompt.
func Run(ctx context.Context, m model.Model, reg *tools.Registry, system, userPrompt string, opts Options) (string, error) {
	return NewSession(m, reg, system).Prompt(ctx, userPrompt, opts)
}

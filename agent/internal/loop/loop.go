// Package loop implements the core agent loop: send the conversation to the
// model, execute any requested tools, feed the results back, and repeat
// until the model answers with text only — or the turn budget runs out.
package loop

import (
	"context"
	"fmt"
	"strings"

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

// TurnResult is the outcome of one Prompt: the assistant's final text plus
// the accounting hosts need for cost telemetry (summed over every model
// round inside the turn).
type TurnResult struct {
	Text   string
	Rounds int
	Usage  model.Usage
}

// Options bounds one Prompt.
type Options struct {
	MaxTurns int
	// Perms decides whether a tool may run; nil approves everything (the
	// caller — CLI main or the ACP server — owns policy).
	Perms perm.Checker
	// OnEvent receives progress events; nil disables.
	OnEvent func(Event)
	// CompactThresholdTokens triggers auto-compact when the last round's
	// context size (Usage.ContextTokens) exceeds it. 0 → DefaultCompact-
	// Threshold; negative → compaction disabled.
	CompactThresholdTokens int
	// KeepRecentMessages is how many trailing messages auto-compact keeps
	// verbatim. 0 → DefaultKeepRecent.
	KeepRecentMessages int
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
// final text plus usage accounting. Tool execution errors are reported back
// to the model as error tool_results, so a failing tool does not abort the
// run — the model can correct course. Denied tools (permission layer)
// likewise come back as error results.
func (s *Session) Prompt(ctx context.Context, userText string, opts Options) (TurnResult, error) {
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
	var result TurnResult
	lastCtx := 0 // context size reported by the previous round
	for turn := 1; turn <= opts.MaxTurns; turn++ {
		if turn > 1 && opts.CompactThresholdTokens >= 0 && lastCtx > opts.CompactThresholdTokens {
			keep := opts.KeepRecentMessages
			if keep <= 0 {
				keep = DefaultKeepRecent
			}
			s.compact(ctx, keep)
			lastCtx = 0 // compacted; don't re-trigger on the same overshoot
		}
		resp, err := s.m.Complete(ctx, model.Request{
			System:   s.system,
			Messages: s.messages,
			Tools:    s.reg.Defs(),
		})
		if err != nil {
			return TurnResult{}, fmt.Errorf("model round %d: %w", turn, err)
		}
		s.messages = append(s.messages, resp.Message)
		result.Rounds++
		result.Usage.InputTokens += resp.Usage.InputTokens
		result.Usage.OutputTokens += resp.Usage.OutputTokens
		result.Usage.CacheReadTokens += resp.Usage.CacheReadTokens
		result.Usage.CacheCreationTokens += resp.Usage.CacheCreationTokens
		lastCtx = resp.Usage.ContextTokens()

		uses := resp.Message.ToolUses()
		if len(uses) == 0 {
			result.Text = resp.Message.Text()
			emit(Event{Kind: EventText, Text: result.Text})
			return result, nil
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
	return TurnResult{}, fmt.Errorf("turn budget exhausted after %d rounds without a final answer", opts.MaxTurns)
}

// Run is the one-shot convenience form: a fresh Session driven by a single
// prompt.
func Run(ctx context.Context, m model.Model, reg *tools.Registry, system, userPrompt string, opts Options) (TurnResult, error) {
	return NewSession(m, reg, system).Prompt(ctx, userPrompt, opts)
}

// Transcript renders the accumulated conversation as readable text for
// post-turn processing (e.g. the memory reflection pass). Tool results are
// truncated to keep the text bounded; the consumer applies its own cap too.
func (s *Session) Transcript() string {
	const toolCap = 500
	var b strings.Builder
	for _, m := range s.messages {
		switch m.Role {
		case model.RoleUser:
			for _, blk := range m.Blocks {
				switch blk.Type {
				case model.BlockToolResult:
					out := blk.Text
					if len(out) > toolCap {
						out = out[:toolCap] + "…"
					}
					fmt.Fprintf(&b, "TOOL result: %s\n", out)
				default:
					if txt := m.Text(); txt != "" {
						fmt.Fprintf(&b, "USER: %s\n", txt)
						break // one USER line per message
					}
				}
				break // only render the first text/tool block per user message
			}
		case model.RoleAssistant:
			for _, blk := range m.Blocks {
				switch blk.Type {
				case model.BlockToolUse:
					fmt.Fprintf(&b, "TOOL %s(%s)\n", blk.Name, string(blk.Input))
				case model.BlockText:
					fmt.Fprintf(&b, "ASSISTANT: %s\n", blk.Text)
				}
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

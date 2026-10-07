// Package loop implements the core agent loop: send the conversation to the
// model, execute any requested tools, feed the results back, and repeat
// until the model answers with text only. A round cap is optional
// (Options.MaxTurns / NIUNIU_AGENT_MAX_TURNS); without one, auto-compact
// and host cancel/timeout are the session's real bounds.
package loop

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/perm"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// envMaxTurns reads the optional NIUNIU_AGENT_MAX_TURNS round cap. Unset,
// invalid, or <= 0 → 0: the loop treats a non-positive cap as unlimited.
func envMaxTurns() int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("NIUNIU_AGENT_MAX_TURNS")))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// EventKind identifies a progress event emitted while a Prompt is in flight.
type EventKind int

const (
	// EventText carries the assistant's final text for one model round.
	EventText EventKind = iota
	// EventToolStart fires before a tool executes.
	EventToolStart
	// EventToolEnd fires after a tool executes (IsError marks failure).
	EventToolEnd
	// EventThinking carries one reasoning (chain-of-thought) block from the
	// assistant; hosts surface it as ACP agent_thought_chunk.
	EventThinking
)

// Event is one progress event; the ACP server maps these onto
// session/update notifications. Text is only set for EventText; ToolName/
// ToolID/ToolInput for start; ToolOutput for end.
type Event struct {
	Kind EventKind
	Text string
	// Delta marks incremental (streaming) text/thinking events; hosts
	// append, non-delta events replace.
	Delta      bool
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
	// MaxTurns caps the model round-trips of one Prompt (each may execute
	// several tools). <= 0 means unlimited unless NIUNIU_AGENT_MAX_TURNS
	// sets a positive cap — auto-compact and host cancel/timeout bound the
	// session instead.
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
	// ContextEditing opts into provider-side server context management
	// (anthropic clear_tool_uses); ignored by the openai family, where the
	// local eviction pass remains the fallback. Opt-in via
	// NIUNIU_AGENT_CONTEXT_EDITING=1.
	ContextEditing bool
	// HistoryDir archives the messages each compaction removes (one JSON
	// chunk per message) for the HistorySearch tool to retrieve on demand.
	// Usually <cwd>/.niuniu-agent/history. Empty → no archive.
	HistoryDir string
	// CompactStatePath optionally persists the merged structured compaction
	// state as JSON (usually <cwd>/.niuniu-agent/session-state.json) so exact
	// details survive on disk and the agent can Read them back after a
	// compaction. Empty → state lives only in the context message.
	CompactStatePath string
	// KeepRecentMessages is how many trailing messages auto-compact keeps
	// verbatim. 0 → DefaultKeepRecent.
	KeepRecentMessages int
	// Thinking carries the reasoning budget/effort applied to every model
	// round of this prompt (session-stable → cache-friendly).
	Thinking model.ThinkingConfig
	// Stream enables SSE streaming: text/thinking deltas are emitted as
	// incremental EventText/EventThinking events (ACP chunk level), and the
	// whole-block events are suppressed to avoid duplicates.
	Stream bool
	// EvictToolResults keeps only the N most recent tool_results verbatim;
	// older ones are truncated to EvictKeepBytes with a one-shot marker.
	// This is the cheap, cache-friendly tier of context management: it runs
	// once per result (never rewrites marked text) and defers to full
	// compaction. 0 = disabled.
	EvictToolResults int
	// EvictKeepBytes caps one evicted tool_result. 0 → 1KB.
	EvictKeepBytes int
}

// Session is a continuing conversation: successive Prompts accumulate
// message history so follow-up questions keep context. Safe for one
// in-flight Prompt at a time (hosts serialize).
type Session struct {
	m        model.Model
	reg      *tools.Registry
	system   string
	messages []model.Message
	// state accumulates the structured compaction state across chained
	// auto-compacts (decisions/files merge, open items refresh).
	state *CompactState
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
	return s.PromptBlocks(ctx, []model.Block{{Type: model.BlockText, Text: userText}}, opts)
}

// SetModel swaps the session's model (session/set_model profile switch).
// Takes effect on the next Prompt round; history is preserved.
func (s *Session) SetModel(m model.Model) { s.m = m }

// PromptBlocks is Prompt with arbitrary user content blocks (text + images
// for multimodal turns).
func (s *Session) PromptBlocks(ctx context.Context, userBlocks []model.Block, opts Options) (TurnResult, error) {
	if opts.MaxTurns <= 0 {
		opts.MaxTurns = envMaxTurns()
	}
	if opts.Perms == nil {
		opts.Perms = perm.AllowAllChecker()
	}
	// 0 → DefaultCompactThreshold (the field's documented default). Leaving
	// the zero value as a literal threshold 0 would make "estimate > 0" always
	// true from the second round on — every turn would run the summarizer and
	// squash the history, which looked like "no response" to the host.
	if opts.CompactThresholdTokens == 0 {
		opts.CompactThresholdTokens = DefaultCompactThreshold
	}
	emit := func(e Event) {
		if opts.OnEvent != nil {
			opts.OnEvent(e)
		}
	}
	s.messages = append(s.messages, model.Message{Role: model.RoleUser, Blocks: userBlocks})
	var result TurnResult
	lastCtx := 0 // context size reported by the previous round
	for turn := 1; ; turn++ {
		if opts.MaxTurns > 0 && turn > opts.MaxTurns {
			return TurnResult{}, fmt.Errorf("turn budget exhausted after %d rounds without a final answer", opts.MaxTurns)
		}
		if opts.EvictToolResults > 0 {
			s.evictOldToolResults(opts.EvictToolResults, opts.EvictKeepBytes)
		}
		// Provider-reported context size drives the trigger; when the
		// gateway doesn't report (lastCtx stays 0), fall back to the local
		// estimator so compaction still fires before hard failure.
		ctxEstimate := lastCtx
		if ctxEstimate == 0 {
			ctxEstimate = EstimateMessagesTokens(s.system, s.messages)
		}
		if turn > 1 && opts.CompactThresholdTokens >= 0 && ctxEstimate > opts.CompactThresholdTokens {
			keep := opts.KeepRecentMessages
			if keep <= 0 {
				keep = DefaultKeepRecent
			}
			slog.Info("loop: auto-compact triggered", "turn", turn,
				"ctxEstimate", ctxEstimate, "threshold", opts.CompactThresholdTokens)
			s.compact(ctx, keep, opts.CompactStatePath, opts.HistoryDir)
			lastCtx = 0 // compacted; don't re-trigger on the same overshoot
		}
		req := model.Request{
			System:         s.system,
			Messages:       s.messages,
			Tools:          s.reg.Defs(),
			Thinking:       opts.Thinking,
			ContextEditing: opts.ContextEditing,
		}
		var textDeltas int
		if opts.Stream {
			req.Stream = func(d model.StreamDelta) {
				kind := EventText
				if d.Kind == model.StreamThinking {
					kind = EventThinking
				}
				if kind == EventText {
					textDeltas++
				}
				emit(Event{Kind: kind, Text: d.Text, Delta: true})
			}
		}
		roundStarted := time.Now()
		resp, err := s.m.Complete(ctx, req)
		if err != nil {
			slog.Error("loop: model round failed", "round", turn, "ms", time.Since(roundStarted).Milliseconds(), "err", err)
			return TurnResult{}, fmt.Errorf("model round %d: %w", turn, err)
		}
		slog.Info("loop: model round done", "round", turn,
			"ms", time.Since(roundStarted).Milliseconds(),
			"stop", resp.StopReason,
			"in", resp.Usage.InputTokens+resp.Usage.CacheReadTokens+resp.Usage.CacheCreationTokens,
			"out", resp.Usage.OutputTokens)
		s.messages = append(s.messages, resp.Message)
		if textDeltas == 0 {
			for _, blk := range resp.Message.Blocks {
				if blk.Type == model.BlockThinking && blk.Text != "" {
					emit(Event{Kind: EventThinking, Text: blk.Text})
				}
			}
		}
		result.Rounds++
		result.Usage.InputTokens += resp.Usage.InputTokens
		result.Usage.OutputTokens += resp.Usage.OutputTokens
		result.Usage.CacheReadTokens += resp.Usage.CacheReadTokens
		result.Usage.CacheCreationTokens += resp.Usage.CacheCreationTokens
		lastCtx = resp.Usage.ContextTokens()

		uses := resp.Message.ToolUses()
		if len(uses) == 0 {
			result.Text = resp.Message.Text()
			if textDeltas == 0 {
				emit(Event{Kind: EventText, Text: result.Text})
			}
			return result, nil
		}

		results := make([]model.Block, len(uses))
		if allAgentUses(uses) && len(uses) > 1 {
			// Parallel fan-out: synchronous Agent calls in one round run
			// concurrently — child sessions are fully independent, so this
			// is safe, and a delegation round should not serialize. Results
			// are backfilled in the original order.
			var wg sync.WaitGroup
			for i, use := range uses {
				emit(Event{Kind: EventToolStart, ToolName: use.Name, ToolID: use.ID, ToolInput: string(use.Input)})
				wg.Add(1)
				go func(i int, use model.Block) {
					defer wg.Done()
					results[i] = s.execToolUse(ctx, use, opts, emit)
				}(i, use)
			}
			wg.Wait()
		} else {
			for i, use := range uses {
				emit(Event{Kind: EventToolStart, ToolName: use.Name, ToolID: use.ID, ToolInput: string(use.Input)})
				results[i] = s.execToolUse(ctx, use, opts, emit)
			}
		}
		s.messages = append(s.messages, model.Message{Role: model.RoleUser, Blocks: results})
	}
}

// execToolUse executes one tool_use with permission gating and error
// backfill, emitting the matching events.
func (s *Session) execToolUse(ctx context.Context, use model.Block, opts Options, emit func(Event)) model.Block {
	decision := opts.Perms.Check(use.Name)
	if decision == perm.Ask {
		// No interactive escalation is wired in this context; treat like a
		// denial (the ACP server never uses a plain Ask).
		decision = perm.Deny
	}
	if decision == perm.Deny {
		emit(Event{Kind: EventToolEnd, ToolName: use.Name, ToolID: use.ID, ToolOutput: perm.DenyMessage, IsError: true})
		return model.Block{Type: model.BlockToolResult, ToolUseID: use.ID, Text: wrapToolResult(perm.DenyMessage, reminderDenied), IsError: true}
	}
	var text string
	var images []model.Block
	if ir, ok := s.reg.Lookup(use.Name); ok {
		if im, implements := ir.(tools.ImageResult); implements {
			var err error
			text, images, err = im.ExecuteWithImages(ctx, use.Input)
			if err != nil {
				emit(Event{Kind: EventToolEnd, ToolName: use.Name, ToolID: use.ID, ToolOutput: err.Error(), IsError: true})
				return model.Block{Type: model.BlockToolResult, ToolUseID: use.ID, Text: wrapToolResult("ERROR: "+err.Error(), reminderError), IsError: true}
			}
		}
	}
	if images == nil {
		out, err := s.reg.Execute(ctx, use.Name, use.Input)
		if err != nil {
			emit(Event{Kind: EventToolEnd, ToolName: use.Name, ToolID: use.ID, ToolOutput: err.Error(), IsError: true})
			return model.Block{Type: model.BlockToolResult, ToolUseID: use.ID, Text: wrapToolResult("ERROR: "+err.Error(), reminderError), IsError: true}
		}
		text = out
	}
	emit(Event{Kind: EventToolEnd, ToolName: use.Name, ToolID: use.ID, ToolOutput: text})
	tr := model.Block{Type: model.BlockToolResult, ToolUseID: use.ID, Text: text}
	for _, im := range images {
		tr.Media, tr.MIME = im.Media, im.MIME
		break // one image per result for now
	}
	return tr
}

// allAgentUses reports whether every tool_use in the round targets the
// Agent tool (the parallel fan-out condition).
func allAgentUses(uses []model.Block) bool {
	for _, u := range uses {
		if u.Name != "Agent" {
			return false
		}
	}
	return true
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

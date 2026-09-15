// Package loop implements the core agent loop: send the conversation to the
// model, execute any requested tools, feed the results back, and repeat
// until the model answers with text only — or the turn budget runs out.
package loop

import (
	"context"
	"fmt"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// DefaultMaxTurns is the model round-trip budget when Options.MaxTurns is
// unset. Each turn may execute several tools.
const DefaultMaxTurns = 16

// Options bounds one Run.
type Options struct {
	MaxTurns int
}

// Run drives the agent loop and returns the assistant's final text answer.
// Tool execution errors are reported back to the model as error
// tool_results, so a failing tool does not abort the run — the model can
// correct course.
func Run(ctx context.Context, m model.Model, reg *tools.Registry, system, userPrompt string, opts Options) (string, error) {
	if opts.MaxTurns <= 0 {
		opts.MaxTurns = DefaultMaxTurns
	}
	messages := []model.Message{{
		Role:   model.RoleUser,
		Blocks: []model.Block{{Type: model.BlockText, Text: userPrompt}},
	}}
	for turn := 1; turn <= opts.MaxTurns; turn++ {
		resp, err := m.Complete(ctx, model.Request{
			System:   system,
			Messages: messages,
			Tools:    reg.Defs(),
		})
		if err != nil {
			return "", fmt.Errorf("model round %d: %w", turn, err)
		}
		messages = append(messages, resp.Message)

		uses := resp.Message.ToolUses()
		if len(uses) == 0 {
			return resp.Message.Text(), nil
		}

		results := make([]model.Block, 0, len(uses))
		for _, use := range uses {
			out, err := reg.Execute(ctx, use.Name, use.Input)
			if err != nil {
				results = append(results, model.Block{
					Type:      model.BlockToolResult,
					ToolUseID: use.ID,
					Text:      "ERROR: " + err.Error(),
					IsError:   true,
				})
				continue
			}
			results = append(results, model.Block{
				Type:      model.BlockToolResult,
				ToolUseID: use.ID,
				Text:      out,
			})
		}
		messages = append(messages, model.Message{Role: model.RoleUser, Blocks: results})
	}
	return "", fmt.Errorf("turn budget exhausted after %d rounds without a final answer", opts.MaxTurns)
}

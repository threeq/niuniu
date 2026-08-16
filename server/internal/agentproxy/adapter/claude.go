package adapter

import "encoding/json"

// ClaudeAdapter implements Adapter for the Claude Code CLI stream-json
// protocol. Behavior is bit-identical to the pre-M2.5.1 agentproxy.ParseStreamLine.
type ClaudeAdapter struct{}

func (ClaudeAdapter) Type() Type { return TypeClaude }

func (ClaudeAdapter) ParseLine(line string) ([]ParsedEvent, error) {
	ev, err := ParseStreamLine(line)
	if err != nil {
		return nil, err
	}
	return []ParsedEvent{ev}, nil
}

// ParseStreamLine parses a single NDJSON line from Claude Code stream-json
// output. Exposed at the package level so the agentproxy package can import
// it via type alias for back-compat with existing call sites.
func ParseStreamLine(line string) (ParsedEvent, error) {
	var raw rawLine
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return ParsedEvent{}, err
	}

	ev := ParsedEvent{
		Type:            raw.Type,
		Subtype:         raw.Subtype,
		SessionID:       raw.SessionID,
		ParentToolUseId: raw.ParentToolUseID,
	}

	switch raw.Type {
	case "system":
		switch raw.Subtype {
		case "api_retry":
			ev.RetryAttempt = raw.Attempt
			ev.RetryError = raw.Error
		}

	case "assistant":
		// Assistant lines carry message.usage with ONE request's token counts
		// (input + cache read/creation). This is the same per-request signal as
		// stream message_start, and the only one available when an
		// Anthropic-compatible gateway omits stream usage (火山方舟/百炼) — the
		// CLI attaches it to the completed assistant message regardless.
		if u := raw.Message.Usage; u != nil {
			ev.InputTokens = u.InputTokens
			ev.OutputTokens = u.OutputTokens
			ev.CacheCreationTokens = u.CacheCreationInputTokens
			ev.CacheReadTokens = u.CacheReadInputTokens
		}
		if raw.Message.Content.Text != "" {
			ev.TextBlocks = append(ev.TextBlocks, TextBlock{Text: raw.Message.Content.Text})
		}
		for _, block := range raw.Message.Content.Blocks {
			switch block.Type {
			case "text":
				if block.Text != "" {
					ev.TextBlocks = append(ev.TextBlocks, TextBlock{Text: block.Text})
				}
			case "tool_use":
				ev.ToolUseBlocks = append(ev.ToolUseBlocks, ToolUseBlock{
					Id:    block.Id,
					Name:  block.Name,
					Input: string(block.Input),
				})
			case "thinking":
				if block.Thinking != "" {
					ev.ThinkingBlocks = append(ev.ThinkingBlocks, ThinkingBlock{
						Thinking: block.Thinking,
					})
				}
			}
		}

	case "user":
		if raw.Message.Content.Text != "" {
			ev.TextBlocks = append(ev.TextBlocks, TextBlock{Text: raw.Message.Content.Text})
		}
		for _, block := range raw.Message.Content.Blocks {
			if block.Type == "tool_result" {
				ev.ToolResults = append(ev.ToolResults, ToolResultBlock{
					ToolUseId: block.ToolUseId,
					Content:   block.Content.Text,
					IsError:   block.IsError,
				})
			}
		}

	case "stream_event":
		if raw.Event != nil {
			se := raw.Event
			ev.StreamEventType = se.Type
			ev.BlockIndex = se.Index

			switch se.Type {
			case "message_start":
				if se.Message != nil && se.Message.Usage != nil {
					ev.InputTokens = se.Message.Usage.InputTokens
					ev.OutputTokens = se.Message.Usage.OutputTokens
					ev.CacheCreationTokens = se.Message.Usage.CacheCreationInputTokens
					ev.CacheReadTokens = se.Message.Usage.CacheReadInputTokens
				}
			case "message_delta":
				if se.Usage != nil {
					ev.OutputTokens = se.Usage.OutputTokens
				}
			case "content_block_start":
				if se.ContentBlock != nil {
					ev.BlockType = se.ContentBlock.Type
					ev.ToolUseName = se.ContentBlock.Name
					ev.ToolUseId = se.ContentBlock.Id
				}
			case "content_block_delta":
				if se.Delta != nil {
					ev.DeltaType = se.Delta.Type
					switch se.Delta.Type {
					case "text_delta":
						ev.DeltaText = se.Delta.Text
					case "input_json_delta":
						ev.DeltaText = se.Delta.PartialJSON
					case "thinking_delta":
						ev.DeltaText = se.Delta.Thinking
					}
				}
			}
		}

	case "result":
		ev.Result = raw.Result
		ev.IsError = raw.IsError
		ev.TotalCostUSD = raw.TotalCostUSD
		ev.NumTurns = raw.NumTurns
		ev.DurationMs = raw.DurationMs
		if raw.Usage != nil {
			ev.InputTokens = raw.Usage.InputTokens
			ev.OutputTokens = raw.Usage.OutputTokens
			ev.CacheCreationTokens = raw.Usage.CacheCreationInputTokens
			ev.CacheReadTokens = raw.Usage.CacheReadInputTokens
		}

	case "rate_limit_event":
		if raw.RateLimitInfo != nil {
			ev.RateLimitRawJSON = string(*raw.RateLimitInfo)
			if err := json.Unmarshal(*raw.RateLimitInfo, &raw.RateLimitInfoParsed); err == nil {
				info := raw.RateLimitInfoParsed
				ev.RateLimitStatus = info.Status
				ev.RateLimitType = info.RateLimitType
				ev.RateLimitResetsAt = info.ResetsAt
				ev.RateLimitOverage = info.OverageStatus
			}
		}
	}

	return ev, nil
}
